# WebRTC PoC Setup Guide

## Quick Setup (Recommended: Go + Pion)

### 1. Initialize Go Project
```bash
mkdir webrtc-poc
cd webrtc-poc
go mod init webrtc-poc
```

### 2. Install Dependencies
```bash
go get github.com/pion/webrtc/v4
go get github.com/gorilla/websocket
go get github.com/pion/interceptor
go get github.com/pion/interceptor/pkg/intervalpli
go get github.com/pion/interceptor/pkg/nack
```

### 3. Create go.mod file
```go
module webrtc-poc

go 1.21

require (
    github.com/gorilla/websocket v1.5.0
    github.com/pion/interceptor v0.1.25
    github.com/pion/webrtc/v4 v4.0.0-beta.21
)
```

### 4. Run the Server
```bash
go run main.go
```

### 5. Access the Application

**✅ For localhost testing:**
Navigate to `https://localhost:8080`

**✅ For network testing (different devices):**
1. Find your server's IP address:
   ```bash
   # Windows: ipconfig
   # Mac/Linux: ifconfig or ip addr show
   ```
2. Navigate to `https://YOUR_SERVER_IP:8080`

**⚠️ Certificate Warning:**
- You'll see a "Not Secure" or "Certificate Error" warning
- Click "Advanced" → "Proceed to localhost (unsafe)" or similar
- This is normal for self-signed certificates in development

## Why HTTPS is Required

Modern browsers require HTTPS for:
- ✅ Camera and microphone access (`getUserMedia()`)
- ✅ WebRTC in general on non-localhost connections
- ✅ Service workers and other secure features

The server now automatically:
- 🔐 Generates a self-signed certificate
- 🔐 Serves HTTPS on port 8080
- 🔐 Includes your local IP addresses in the certificate
- 🔐 Uses secure WebSocket (WSS) connections

## Browser Certificate Instructions

### Chrome/Edge:
1. You'll see "Your connection is not private"
2. Click **"Advanced"**
3. Click **"Proceed to [IP] (unsafe)"**

### Firefox:
1. You'll see "Warning: Potential Security Risk Ahead"
2. Click **"Advanced..."**
3. Click **"Accept the Risk and Continue"**

### Safari:
1. You'll see "This Connection Is Not Private"
2. Click **"Show Details"**
3. Click **"visit this website"**
4. Click **"Visit Website"**

## 🔧 WebSocket Certificate Fix

**Important**: Even after accepting the certificate for the web page, WebSocket connections (WSS) may still fail. Here's how to fix:

### Method 1: Accept WSS Certificate
1. **Click "Test WebSocket Connection"** button on the page
2. If it fails, **click the certificate link** shown on the page
3. **Accept the certificate** in the new tab
4. **Go back** and test again until it works

### Method 2: Manual Certificate Accept
1. Open new tab: `https://your-server-ip:8080/ws-test`
2. Accept the security warning
3. Return to main page and try connection

### Method 3: Use HTTP for Local Testing Only
- Access via: `http://your-server-ip:8081` (note the different port)
- ⚠️ Camera access will only work on localhost with HTTP

## Testing Steps

1. **Start server**: `go run main.go`
2. **Accept certificate warning** in browser for the main page
3. **🔧 IMPORTANT: Test WebSocket connection**:
   - Click **"Test WebSocket Connection"** button  
   - If it fails, follow the certificate fix steps above
   - Repeat until WebSocket test passes ✅
4. **Click "Start WebRTC Connection"**
5. **Allow camera/microphone access** when prompted
6. **Wait for "Ready for API calls" status**
7. **Test API buttons** - should work now!

## Network Testing

**From another device on same network:**
```bash
# Find server IP first
ip addr show  # Linux/Mac
ipconfig      # Windows

# Then access from other device:
https://192.168.1.XXX:8080
```

## Generated Files

The server creates these files automatically:
- `server.crt` - SSL certificate
- `server.key` - Private key

**Don't commit these to git!** Add to `.gitignore`:
```gitignore
server.crt
server.key
```

### ✅ Data Channel REST API Emulation
- **GET /api/health** - Health check endpoint
- **GET /api/users** - Get users list
- **POST /api/users** - Create new user
- Real-time request/response over WebRTC DataChannel

### ✅ Video & Audio Streaming
- Local camera/microphone capture
- Bidirectional media streaming
- Echo server functionality (server echoes back received media)

### ✅ WebRTC Infrastructure
- Signaling via WebSocket
- ICE candidate exchange
- STUN server for NAT traversal

## Alternative Implementations

### JavaScript/Node.js Option
```bash
npm install node-datachannel ws express
```

### Python Option
```bash
pip install aiortc aiohttp websockets
```

### C++ Option (Direct libdatachannel)
```bash
# Ubuntu/Debian
apt-get install libdatachannel-dev

# Build from source
git clone https://github.com/paullouisageneau/libdatachannel.git
cd libdatachannel
cmake -B build -DUSE_GNUTLS=0 -DUSE_NICE=0
cmake --build build -j$(nproc)
```

## Architecture Overview

```
┌─────────────────┐    WebSocket     ┌─────────────────┐
│   Browser       │◄────────────────►│   Go Server     │
│   (Client)      │                  │                 │
│                 │    WebRTC        │                 │
│ • Video/Audio   │◄────────────────►│ • Media Echo    │
│ • DataChannel   │                  │ • REST API      │
│ • REST Client   │                  │ • DataChannel   │
└─────────────────┘                  └─────────────────┘
```

## Why Go + Pion for PoC?

1. **Fast Development**: Quick setup, extensive examples
2. **Production Ready**: Used by many companies in production
3. **Cross Platform**: Single binary deployment
4. **Active Community**: Responsive support and documentation
5. **No CGo Dependencies**: Easier compilation and deployment
6. **Built-in Testing**: Extensive test suite and examples

## Testing Your PoC

1. **Data Channel API**: Click the API buttons to test REST-like communication
2. **Media Streaming**: Verify video/audio capture and echo functionality  
3. **Connection Stability**: Test reconnection and error handling
4. **Performance**: Monitor latency and throughput

## Next Steps for Production

- Add authentication and authorization
- Implement proper signaling server (not WebSocket-based)
- Add error handling and reconnection logic
- Optimize media codecs and quality
- Add monitoring and logging
- Scale with SFU (Selective Forwarding Unit) for multiple participants

## Troubleshooting

- **No video/audio**: Check browser permissions
- **Connection fails**: Verify STUN server access
- **Data channel not working**: Check WebSocket connection
- **Build errors**: Ensure Go 1.21+ is installed
