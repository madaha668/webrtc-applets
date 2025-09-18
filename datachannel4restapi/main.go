package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/intervalpli"
	"github.com/pion/interceptor/pkg/nack"
	"github.com/pion/webrtc/v4"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for PoC
	},
}

type Client struct {
	conn       *websocket.Conn
	peerConn   *webrtc.PeerConnection
	dataChannel *webrtc.DataChannel
	mu         sync.Mutex
}

type RestAPIMessage struct {
	Method   string            `json:"method"`
	Endpoint string            `json:"endpoint"`
	Headers  map[string]string `json:"headers"`
	Body     interface{}       `json:"body"`
}

type RestAPIResponse struct {
	Status  int         `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    interface{} `json:"body"`
}

// filteredWriter filters out harmless TLS handshake error messages
type filteredWriter struct{}

func (w *filteredWriter) Write(p []byte) (n int, err error) {
	s := string(p)
	// Filter out TLS handshake errors - they're just browsers making HTTP requests to HTTPS port
	if !strings.Contains(s, "TLS handshake error") && 
	   !strings.Contains(s, "client sent an HTTP request to an HTTPS server") {
		return os.Stderr.Write(p)
	}
	return len(p), nil
}

func main() {
	// Generate self-signed certificate for HTTPS
	if err := generateCertificate(); err != nil {
		log.Fatal("Failed to generate certificate:", err)
	}

	// Create a MediaEngine object to configure the supported codec
	m := &webrtc.MediaEngine{}

	// Setup the codecs you want to use.
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000, Channels: 0, SDPFmtpLine: "", RTCPFeedback: nil},
		PayloadType:        96,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		panic(err)
	}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 0, SDPFmtpLine: "", RTCPFeedback: nil},
		PayloadType:        111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		panic(err)
	}

	// Create a InterceptorRegistry. This is the user configurable RTP/RTCP Pipeline.
	i := &interceptor.Registry{}

	// Use the default set of Interceptors
	if err := webrtc.RegisterDefaultInterceptors(m, i); err != nil {
		panic(err)
	}

	// Add NACK and PLI interceptors
	nackGenerator, err := nack.NewGeneratorInterceptor()
	if err != nil {
		panic(err)
	}
	i.Add(nackGenerator)

	pliGenerator, err := intervalpli.NewReceiverInterceptor()
	if err != nil {
		panic(err)
	}
	i.Add(pliGenerator)

	// Create the API object with the MediaEngine
	api := webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(i))

	// HTTP handlers
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		handleWebSocket(w, r, api)
	})

	http.HandleFunc("/ws-test", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("WebSocket endpoint is working. Certificate accepted for WSS connections."))
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, htmlContent)
	})

	fmt.Println("HTTPS Server starting on :8080")
	fmt.Println("Access via: https://localhost:8080")
	fmt.Println("Or via your IP: https://YOUR_IP:8080")
	fmt.Println("Note: You'll need to accept the self-signed certificate warning")
	
	// Start HTTP redirect server on port 8081
	go func() {
		redirectMux := http.NewServeMux()
		redirectMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			target := "https://" + r.Host + ":8080" + r.URL.Path
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently)
		})
		
		log.Println("HTTP redirect server starting on :8081 (redirects to HTTPS)")
		http.ListenAndServe(":8081", redirectMux)
	}()
	
	// Create custom logger to filter TLS handshake errors
	server := &http.Server{
		Addr:    ":8080",
		Handler: nil,
		ErrorLog: log.New(&filteredWriter{}, "", log.LstdFlags),
	}
	
	log.Fatal(server.ListenAndServeTLS("server.crt", "server.key"))
}

func handleWebSocket(w http.ResponseWriter, r *http.Request, api *webrtc.API) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WebSocket upgrade error:", err)
		return
	}
	defer conn.Close()

	client := &Client{conn: conn}

	// Create a new RTCPeerConnection
	peerConnection, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{
					"stun:stun.l.google.com:19302",
					"stun:stun1.l.google.com:19302", 
					"stun:stun2.l.google.com:19302",
					"stun:stun.cloudflare.com:3478",
				},
			},
		},
	})
	if err != nil {
		log.Println("Failed to create peer connection:", err)
		return
	}
	defer peerConnection.Close()

	// Set up ICE candidate handling
	peerConnection.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			log.Println("ICE gathering completed")
			return
		}
		
		// Get the full candidate JSON with all required fields
		candidateInit := candidate.ToJSON()
		log.Printf("Sending ICE candidate: %s", candidateInit.Candidate)
		
		candidateJSON := map[string]interface{}{
			"type":         "ice-candidate",
			"candidate":    candidateInit.Candidate,
			"sdpMid":       candidateInit.SDPMid,
			"sdpMLineIndex": candidateInit.SDPMLineIndex,
		}
		
		if err := conn.WriteJSON(candidateJSON); err != nil {
			log.Println("Failed to send ICE candidate:", err)
		}
	})

	// Handle connection state changes
	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("Peer connection state changed: %s", state.String())
	})

	// Handle ICE connection state changes  
	peerConnection.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("ICE connection state changed: %s", state.String())
	})

	// Handle incoming data channel from client
	peerConnection.OnDataChannel(func(dataChannel *webrtc.DataChannel) {
		log.Printf("📥 Received data channel from client: %s", dataChannel.Label())
		client.dataChannel = dataChannel

		dataChannel.OnOpen(func() {
			log.Println("✅ Data channel opened on server side")
			
			// Send a welcome REST API response
			welcome := RestAPIResponse{
				Status: 200,
				Headers: map[string]string{"Content-Type": "application/json"},
				Body:   map[string]string{"message": "WebRTC REST API Server Ready"},
			}
			if data, err := json.Marshal(welcome); err == nil {
				log.Println("📤 Sending welcome message to data channel")
				if sendErr := dataChannel.Send(data); sendErr != nil {
					log.Printf("Failed to send welcome message: %v", sendErr)
				}
			}
		})

		dataChannel.OnClose(func() {
			log.Println("❌ Data channel closed on server side")
			client.dataChannel = nil
		})

		dataChannel.OnError(func(err error) {
			log.Printf("⚠️ Data channel error on server side: %v", err)
		})

		dataChannel.OnMessage(func(msg webrtc.DataChannelMessage) {
			log.Printf("📥 Received data channel message: %s", string(msg.Data))
			
			// Parse REST API request
			var apiRequest RestAPIMessage
			if err := json.Unmarshal(msg.Data, &apiRequest); err != nil {
				log.Println("Failed to parse API request:", err)
				return
			}

			// Handle the REST API request
			response := handleRestAPIRequest(apiRequest)
			
			// Send response back
			if data, err := json.Marshal(response); err == nil {
				if sendErr := dataChannel.Send(data); sendErr != nil {
					log.Printf("Failed to send API response: %v", sendErr)
				}
			}
		})
	})

	client.peerConn = peerConnection

	// Add video track
	videoTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "video", "pion")
	if err != nil {
		log.Println("Failed to create video track:", err)
		return
	}

	rtpSender, err := peerConnection.AddTrack(videoTrack)
	if err != nil {
		log.Println("Failed to add video track:", err)
		return
	}

	// Read incoming RTCP packets for video
	go func() {
		rtcpBuf := make([]byte, 1500)
		for {
			if _, _, rtcpErr := rtpSender.Read(rtcpBuf); rtcpErr != nil {
				return
			}
		}
	}()

	// Add audio track
	audioTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "pion")
	if err != nil {
		log.Println("Failed to create audio track:", err)
		return
	}

	rtpSender, err = peerConnection.AddTrack(audioTrack)
	if err != nil {
		log.Println("Failed to add audio track:", err)
		return
	}

	// Handle incoming tracks (from client)
	peerConnection.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		log.Printf("Track has started, of type %d: %s", track.PayloadType(), track.Codec().MimeType)
		
		// Here you can process incoming media
		go func() {
			rtpBuf := make([]byte, 1400)
			for {
				i, _, err := track.Read(rtpBuf)
				if err != nil {
					log.Printf("Track read error: %v", err)
					return
				}
				
				// Echo the media back (simple echo server)
				if track.Kind() == webrtc.RTPCodecTypeVideo {
					if _, writeErr := videoTrack.Write(rtpBuf[:i]); writeErr != nil && writeErr.Error() != "interceptor is not bind" {
						log.Println("Failed to write video:", writeErr)
					} else if writeErr == nil {
						// Log successful video echo (but only occasionally to avoid spam)
						if i%100 == 0 {
							log.Printf("Video packet echoed, size: %d bytes", i)
						}
					}
				} else if track.Kind() == webrtc.RTPCodecTypeAudio {
					if _, writeErr := audioTrack.Write(rtpBuf[:i]); writeErr != nil && writeErr.Error() != "interceptor is not bind" {
						log.Println("Failed to write audio:", writeErr)
					}
				}
			}
		}()
	})

	// Handle WebSocket messages for signaling
	for {
		var msg map[string]interface{}
		err := conn.ReadJSON(&msg)
		if err != nil {
			log.Println("WebSocket read error:", err)
			break
		}

		log.Printf("Received message type: %v", msg["type"])

		switch msg["type"] {
		case "offer":
			handleOffer(client, msg)
		case "answer":
			handleAnswer(client, msg)
		case "ice-candidate":
			handleICECandidate(client, msg)
		}
	}
}

func handleRestAPIRequest(request RestAPIMessage) RestAPIResponse {
	log.Printf("Handling REST API: %s %s", request.Method, request.Endpoint)
	
	// Simulate different REST endpoints
	switch request.Endpoint {
	case "/api/users":
		if request.Method == "GET" {
			return RestAPIResponse{
				Status: 200,
				Headers: map[string]string{"Content-Type": "application/json"},
				Body: []map[string]interface{}{
					{"id": 1, "name": "John Doe", "email": "john@example.com"},
					{"id": 2, "name": "Jane Smith", "email": "jane@example.com"},
				},
			}
		} else if request.Method == "POST" {
			return RestAPIResponse{
				Status: 201,
				Headers: map[string]string{"Content-Type": "application/json"},
				Body: map[string]interface{}{
					"id": 3,
					"message": "User created successfully",
					"data": request.Body,
				},
			}
		}
	case "/api/health":
		return RestAPIResponse{
			Status: 200,
			Headers: map[string]string{"Content-Type": "application/json"},
			Body: map[string]interface{}{
				"status": "healthy",
				"timestamp": "2025-09-18T12:00:00Z",
				"version": "1.0.0",
			},
		}
	default:
		return RestAPIResponse{
			Status: 404,
			Headers: map[string]string{"Content-Type": "application/json"},
			Body: map[string]string{
				"error": "Endpoint not found",
				"endpoint": request.Endpoint,
			},
		}
	}
	
	return RestAPIResponse{
		Status: 405,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body: map[string]string{"error": "Method not allowed"},
	}
}

func handleOffer(client *Client, msg map[string]interface{}) {
	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  msg["sdp"].(string),
	}

	if err := client.peerConn.SetRemoteDescription(offer); err != nil {
		log.Println("Failed to set remote description:", err)
		return
	}

	answer, err := client.peerConn.CreateAnswer(nil)
	if err != nil {
		log.Println("Failed to create answer:", err)
		return
	}

	if err := client.peerConn.SetLocalDescription(answer); err != nil {
		log.Println("Failed to set local description:", err)
		return
	}

	client.conn.WriteJSON(map[string]interface{}{
		"type": "answer",
		"sdp":  answer.SDP,
	})
}

func handleAnswer(client *Client, msg map[string]interface{}) {
	answer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  msg["sdp"].(string),
	}

	if err := client.peerConn.SetRemoteDescription(answer); err != nil {
		log.Println("Failed to set remote description:", err)
	}
}

func handleICECandidate(client *Client, msg map[string]interface{}) {
	candidateStr, ok := msg["candidate"].(string)
	if !ok {
		log.Println("Invalid ICE candidate format")
		return
	}
	
	log.Printf("Received ICE candidate from client: %s", candidateStr)
	
	// Create ICE candidate with proper initialization
	candidate := webrtc.ICECandidateInit{
		Candidate: candidateStr,
	}
	
	// Add sdpMid and sdpMLineIndex if provided
	if sdpMid, ok := msg["sdpMid"].(string); ok {
		candidate.SDPMid = &sdpMid
	}
	if sdpMLineIndex, ok := msg["sdpMLineIndex"].(float64); ok {
		idx := uint16(sdpMLineIndex)
		candidate.SDPMLineIndex = &idx
	}

	if err := client.peerConn.AddICECandidate(candidate); err != nil {
		log.Printf("Failed to add ICE candidate: %v", err)
	} else {
		log.Println("ICE candidate added successfully")
	}
}

func generateCertificate() error {
	// Check if certificate already exists
	if _, err := os.Stat("server.crt"); err == nil {
		if _, err := os.Stat("server.key"); err == nil {
			fmt.Println("Using existing certificate files")
			return nil
		}
	}

	fmt.Println("Generating self-signed certificate...")

	// Generate private key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}

	// Create certificate template
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"WebRTC PoC"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour), // Valid for 1 year
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	// Add localhost and local IP addresses
	template.DNSNames = []string{"localhost"}
	template.IPAddresses = []net.IP{
		net.IPv4(127, 0, 0, 1),
		net.IPv6loopback,
	}

	// Add local network IPs
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				if ipnet.IP.To4() != nil {
					template.IPAddresses = append(template.IPAddresses, ipnet.IP)
				}
			}
		}
	}

	// Create certificate
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return err
	}

	// Save certificate
	certOut, err := os.Create("server.crt")
	if err != nil {
		return err
	}
	defer certOut.Close()

	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		return err
	}

	// Save private key
	keyOut, err := os.Create("server.key")
	if err != nil {
		return err
	}
	defer keyOut.Close()

	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return err
	}

	if err := pem.Encode(keyOut, &pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER}); err != nil {
		return err
	}

	fmt.Println("Certificate generated successfully!")
	return nil
}

const htmlContent = `<!DOCTYPE html>
<html>
<head>
    <title>WebRTC PoC with REST API over DataChannel</title>
    <style>
        body { font-family: Arial, sans-serif; margin: 20px; }
        video { width: 300px; height: 200px; border: 1px solid #ccc; margin: 10px; }
        button { padding: 10px 20px; margin: 5px; }
        .api-section { border: 1px solid #ddd; padding: 15px; margin: 10px 0; }
        .response { background: #f5f5f5; padding: 10px; margin: 10px 0; }
        .status { margin: 10px 0; padding: 10px; background: #f0f0f0; border-radius: 4px; font-weight: bold; }
    </style>
</head>
<body>
    <h1>WebRTC PoC: REST API over DataChannel + Media Streaming</h1>
    
    <div>
        <button onclick="startConnection()">Start WebRTC Connection</button>
        <button onclick="stopConnection()">Stop Connection</button>
        <button onclick="testWebSocket()">Test WebSocket Connection</button>
        <div id="connectionStatus" class="status">Ready to connect</div>
        
        <div style="background: #fff3cd; border: 1px solid #ffeaa7; padding: 10px; margin: 10px 0; border-radius: 4px;">
            <strong>📋 Certificate Setup:</strong><br>
            If you see "WebSocket connection failed", you need to accept the certificate for WebSocket connections:<br>
            1. <a href="/ws-test" target="_blank">Click here to accept WSS certificate</a><br>
            2. Accept the security warning in the new tab<br>
            3. Come back and try "Start WebRTC Connection" again
        </div>
    </div>

    <div>
        <h3>Local Video</h3>
        <video id="localVideo" autoplay muted></video>
        
        <h3>Remote Video</h3>
        <video id="remoteVideo" autoplay></video>
    </div>

    <div class="api-section">
        <h3>REST API over DataChannel</h3>
        <button onclick="apiCall('GET', '/api/health')">GET /api/health</button>
        <button onclick="apiCall('GET', '/api/users')">GET /api/users</button>
        <button onclick="apiCall('POST', '/api/users')">POST /api/users</button>
        
        <div id="apiResponses"></div>
    </div>

    <script>
        let pc = null;
        let dataChannel = null;
        let ws = null;

        function updateConnectionStatus(status) {
            const statusElement = document.getElementById('connectionStatus');
            if (statusElement) {
                console.log('update the Connection status in web page:', status);
                statusElement.textContent = status;
            }
            console.log('Connection status:', status);
        }

        async function startConnection() {
            try {
                updateConnectionStatus('Starting connection...');

                // Get user media
                const stream = await navigator.mediaDevices.getUserMedia({
                    video: true,
                    audio: true
                });
                
                document.getElementById('localVideo').srcObject = stream;
                updateConnectionStatus('Got user media, creating WebSocket...');

                // Create WebSocket connection (use WSS for HTTPS)
                const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
                const wsUrl = protocol + '//' + window.location.host + '/ws';
                console.log('Connecting to WebSocket:', wsUrl);
                ws = new WebSocket(wsUrl);
                
                ws.onopen = () => {
                    updateConnectionStatus('WebSocket connected, creating peer connection...');
                    createPeerConnection(stream);
                };

                ws.onerror = (error) => {
                    console.error('WebSocket error:', error);
                    updateConnectionStatus('WebSocket connection failed - likely certificate issue');
                    
                    // Provide helpful instructions for certificate issues
                    const helpMessage = 'WebSocket connection failed. This is usually due to certificate issues.\n\n' +
                        'To fix this:\n' +
                        '1. Open a new tab\n' +
                        '2. Go to: ' + wsUrl.replace('ws', 'http') + '\n' +
                        '3. Accept the certificate warning\n' +
                        '4. Come back to this tab and try again\n\n' +
                        'Or try accessing via HTTP for testing:\n' +
                        window.location.protocol + '//' + window.location.host.replace(':8080', ':8081');
                    
                    setTimeout(() => {
                        if (confirm('WebSocket connection failed (likely certificate issue). Would you like to see troubleshooting steps?')) {
                            alert(helpMessage);
                        }
                    }, 2000);
                };

                ws.onclose = () => {
                    updateConnectionStatus('WebSocket connection closed');
                };

            } catch (error) {
                console.error('Error starting connection:', error);
                updateConnectionStatus('Failed to get user media');
            }
        }

        async function createPeerConnection(stream) {
            try {
                // Create peer connection with more STUN servers
                pc = new RTCPeerConnection({
                    iceServers: [
                        { urls: 'stun:stun.l.google.com:19302' },
                        { urls: 'stun:stun1.l.google.com:19302' },
                        { urls: 'stun:stun2.l.google.com:19302' },
                        { urls: 'stun:stun.cloudflare.com:3478' }
                    ]
                });

                // Create data channel on client side (offer side)
                console.log('Creating data channel on client side...');
                dataChannel = pc.createDataChannel('rest-api', {
                    ordered: true
                });

                dataChannel.onopen = () => {
                    console.log('✅ Data channel opened on client side');
                    updateConnectionStatus('Data channel connected! Ready for API calls.');
                };
                
                dataChannel.onclose = () => {
                    console.log('❌ Data channel closed on client side');
                    updateConnectionStatus('Data channel disconnected');
                    dataChannel = null;
                };
                
                dataChannel.onerror = (error) => {
                    console.error('⚠️ Data channel error on client side:', error);
                    updateConnectionStatus('Data channel error: ' + error);
                };
                
                dataChannel.onmessage = (event) => {
                    console.log('📥 Received message on data channel:', event.data);
                    handleDataChannelMessage(event);
                };

                // Add local stream tracks
                stream.getTracks().forEach(track => {
                    pc.addTrack(track, stream);
                });

                // Handle remote stream
                pc.ontrack = (event) => {
                    console.log('Received remote track:', event.track.kind);
                    console.log('Remote streams:', event.streams.length);
                    if (event.streams.length > 0) {
                        console.log('Setting remote video source');
                        const remoteVideo = document.getElementById('remoteVideo');
                        remoteVideo.srcObject = event.streams[0];
                        
                        // Add event listeners to track video status
                        remoteVideo.onloadedmetadata = () => {
                            console.log('Remote video metadata loaded');
                        };
                        remoteVideo.oncanplay = () => {
                            console.log('Remote video can play');
                        };
                        remoteVideo.onerror = (e) => {
                            console.error('Remote video error:', e);
                        };
                    }
                };

                // Handle data channel events (server will receive data channel from client)
                pc.ondatachannel = (event) => {
                    console.log('🎉 Server created data channel, but we handle it on client side');
                    // We don't need to handle server data channels since we create it on client side
                };

                // Handle connection state changes
                pc.onconnectionstatechange = () => {
                    console.log('🔌 Connection state:', pc.connectionState);
                    if (pc.connectionState === 'connected') {
                        updateConnectionStatus('WebRTC connected, waiting for data channel...');
                        
                        // Set a timeout to check if data channel connects
                        setTimeout(() => {
                            if (!dataChannel || dataChannel.readyState !== 'open') {
                                console.warn('⚠️ Data channel not connected after 5 seconds');
                                updateConnectionStatus('Data channel connection timeout - this might be a network issue');
                            }
                        }, 5000);
                        
                    } else if (pc.connectionState === 'failed') {
                        updateConnectionStatus('WebRTC connection failed');
                    } else if (pc.connectionState === 'disconnected') {
                        updateConnectionStatus('WebRTC disconnected');
                    }
                };

                // Handle ICE connection state changes
                pc.oniceconnectionstatechange = () => {
                    console.log('🧊 ICE connection state:', pc.iceConnectionState);
                    if (pc.iceConnectionState === 'connected' || pc.iceConnectionState === 'completed') {
                        console.log('✅ ICE connection established');
                    } else if (pc.iceConnectionState === 'failed') {
                        updateConnectionStatus('ICE connection failed');
                    }
                };

                // Handle ICE candidates with detailed logging
                pc.onicecandidate = (event) => {
                    if (event.candidate) {
                        console.log('Local ICE candidate:', event.candidate.candidate);
                        console.log('Candidate type:', event.candidate.type);
                        console.log('sdpMid:', event.candidate.sdpMid, 'sdpMLineIndex:', event.candidate.sdpMLineIndex);
                        
                        ws.send(JSON.stringify({
                            type: 'ice-candidate',
                            candidate: event.candidate.candidate,
                            sdpMid: event.candidate.sdpMid,
                            sdpMLineIndex: event.candidate.sdpMLineIndex
                        }));
                    } else {
                        console.log('ICE candidate gathering completed');
                    }
                };

                // WebSocket message handler
                ws.onmessage = async (event) => {
                    const message = JSON.parse(event.data);
                    console.log('Received message:', message.type);
                    
                    if (message.type === 'answer') {
                        await pc.setRemoteDescription({
                            type: 'answer',
                            sdp: message.sdp
                        });
                        updateConnectionStatus('Answer received, connecting...');
                    } else if (message.type === 'ice-candidate') {
                        // Skip ICE candidates with null sdpMid/sdpMLineIndex to avoid errors
                        if (message.candidate && message.sdpMid !== null && message.sdpMLineIndex !== null) {
                            console.log('Adding ICE candidate:', message.candidate);
                            console.log('sdpMid:', message.sdpMid, 'sdpMLineIndex:', message.sdpMLineIndex);
                            
                            try {
                                const candidateInit = {
                                    candidate: message.candidate,
                                    sdpMid: message.sdpMid,
                                    sdpMLineIndex: message.sdpMLineIndex
                                };
                                
                                await pc.addIceCandidate(new RTCIceCandidate(candidateInit));
                                console.log('ICE candidate added successfully');
                            } catch (error) {
                                console.error('Failed to add ICE candidate:', error);
                                console.error('Candidate data:', message);
                            }
                        } else {
                            console.log('Skipping ICE candidate with null sdpMid/sdpMLineIndex:', message.candidate);
                        }
                    }
                };

                // Create and send offer
                updateConnectionStatus('Creating offer...');
                const offer = await pc.createOffer();
                await pc.setLocalDescription(offer);
                
                console.log('Sending offer');
                ws.send(JSON.stringify({
                    type: 'offer',
                    sdp: offer.sdp
                }));

            } catch (error) {
                console.error('Error creating peer connection:', error);
                updateConnectionStatus('Failed to create peer connection');
            }
        }

        function testWebSocket() {
            updateConnectionStatus('Testing WebSocket connection...');
            
            const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
            const wsUrl = protocol + '//' + window.location.host + '/ws';
            console.log('Testing WebSocket URL:', wsUrl);
            
            const testWs = new WebSocket(wsUrl);
            
            testWs.onopen = () => {
                console.log('✅ WebSocket test successful');
                updateConnectionStatus('WebSocket test successful! You can now start WebRTC connection.');
                testWs.close();
            };
            
            testWs.onerror = (error) => {
                console.error('❌ WebSocket test failed:', error);
                updateConnectionStatus('WebSocket test failed - certificate issue detected');
                
                alert('WebSocket connection failed!\n\n' +
                    'SOLUTION:\n' +
                    '1. Click the "Click here to accept WSS certificate" link above\n' +
                    '2. Accept the security warning in the new tab\n' +
                    '3. Come back and click "Test WebSocket Connection" again\n' +
                    '4. Once test passes, start WebRTC connection\n\n' +
                    'Current WebSocket URL: ' + wsUrl);
            };
            
            testWs.onclose = () => {
                console.log('WebSocket test connection closed');
            };
            
            // Close test connection after 3 seconds
            setTimeout(() => {
                if (testWs.readyState === WebSocket.OPEN) {
                    testWs.close();
                }
            }, 3000);
        }

        function stopConnection() {
            updateConnectionStatus('Disconnecting...');
            
            if (dataChannel) {
                dataChannel.close();
                dataChannel = null;
            }
            if (pc) {
                pc.close();
                pc = null;
            }
            if (ws) {
                ws.close();
                ws = null;
            }
            
            updateConnectionStatus('Disconnected');
        }

        function apiCall(method, endpoint, body = null) {
            console.log('🔍 API call attempt - checking data channel...');
            console.log('📊 DataChannel exists:', !!dataChannel);
            if (dataChannel) {
                console.log('📊 DataChannel state:', dataChannel.readyState);
            }
            
            if (!dataChannel || dataChannel.readyState !== 'open') {
                alert('Data channel not connected. Please start connection first and wait for "Ready for API calls" status.\n\nCurrent state: ' + (dataChannel ? dataChannel.readyState : 'null'));
                return;
            }

            console.log('📤 Making API call: ' + method + ' ' + endpoint);

            const request = {
                method: method,
                endpoint: endpoint,
                headers: { 'Content-Type': 'application/json' },
                body: body || (method === 'POST' ? { name: 'New User', email: 'newuser@example.com' } : null)
            };

            dataChannel.send(JSON.stringify(request));
        }

        function handleDataChannelMessage(event) {
            console.log('📥 Raw data received:', event.data);
            console.log('📊 Data type:', typeof event.data);
            console.log('📊 Data constructor:', event.data.constructor.name);
            
            let messageText;
            
            // Handle different data types from data channel
            if (typeof event.data === 'string') {
                messageText = event.data;
                console.log('📄 Received as string:', messageText);
            } else if (event.data instanceof ArrayBuffer) {
                // Convert ArrayBuffer to string
                const decoder = new TextDecoder('utf-8');
                messageText = decoder.decode(event.data);
                console.log('🔄 Converted ArrayBuffer to string:', messageText);
            } else if (event.data instanceof Blob) {
                // Handle Blob (shouldn't happen in our case, but good to be safe)
                console.log('📦 Received Blob, converting to text...');
                event.data.text().then(text => {
                    handleParsedMessage(text);
                });
                return; // Exit early, will handle async
            } else {
                console.error('❌ Unexpected data type received:', typeof event.data);
                return;
            }
            
            handleParsedMessage(messageText);
        }
        
        function handleParsedMessage(messageText) {
            try {
                console.log('📥 Parsing JSON:', messageText);
                const response = JSON.parse(messageText);
                console.log('✅ Successfully parsed response:', response);
                
                const responsesDiv = document.getElementById('apiResponses');
                const responseElement = document.createElement('div');
                responseElement.className = 'response';
                responseElement.innerHTML = 
                    '<strong>Status:</strong> ' + response.status + '<br>' +
                    '<strong>Response:</strong><pre>' + JSON.stringify(response.body, null, 2) + '</pre>';
                
                responsesDiv.insertBefore(responseElement, responsesDiv.firstChild);
                
            } catch (error) {
                console.error('❌ Failed to parse JSON:', error);
                console.error('📄 Raw message text:', messageText);
                
                // Show raw response in case of parse error
                const responsesDiv = document.getElementById('apiResponses');
                const responseElement = document.createElement('div');
                responseElement.className = 'response';
                responseElement.style.background = '#ffe6e6';
                responseElement.innerHTML = 
                    '<strong>⚠️ JSON Parse Error:</strong><br>' +
                    '<strong>Error:</strong> ' + error.message + '<br>' +
                    '<strong>Raw Data:</strong><pre>' + messageText + '</pre>';
                
                responsesDiv.insertBefore(responseElement, responsesDiv.firstChild);
            }
        }

        // Debug function to manually check data channel status
        function checkDataChannelStatus() {
            console.log('=== Data Channel Debug Info ===');
            console.log('pc:', !!pc);
            console.log('dataChannel:', !!dataChannel);
            if (pc) {
                console.log('PC connection state:', pc.connectionState);
                console.log('PC ICE state:', pc.iceConnectionState);
            }
            if (dataChannel) {
                console.log('DC readyState:', dataChannel.readyState);
                console.log('DC label:', dataChannel.label);
            }
            console.log('===============================');
        }

        // Call this function periodically to debug
        setInterval(checkDataChannelStatus, 10000); // Every 10 seconds
    </script>
</body>
</html>`
