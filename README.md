# EAG ExtProc Service

A minimal external processing service for Gloo Edge that adds custom headers to HTTP requests.

## What is ExtProc?

External Processing (ExtProc) is a feature in Gloo Edge that allows you to intercept and modify HTTP requests/responses using a custom service. Instead of writing complex Envoy filters, you write a simple gRPC service that Gloo calls for each request.

## How It Works

```
Client Request → Gloo Gateway → ExtProc Service → Backend API
                      ↓              ↓
                   Headers        Add Header
                   Sent Here      Instructions
```

1. **Client** makes HTTP request to your API
2. **Gloo Gateway** intercepts the request
3. **Gloo** sends request headers to your ExtProc service via gRPC
4. **ExtProc Service** processes headers and responds with modifications
5. **Gloo** applies the modifications and forwards to your backend
6. **Backend** receives the original request + your custom headers

## What This Service Does

This minimal service adds **one header** to every HTTP request:

```
x-processed-by: eag-extproc
```

## Project Structure

```
.
├── main.go          # ExtProc service implementation
├── go.mod           # Go dependencies
├── Dockerfile       # Container build instructions
└── README.md        # This documentation
```

## Prerequisites

- Gloo Edge Enterprise installed in Kubernetes
- Docker for building the service
- kubectl access to your cluster

## Quick Start

### 1. Build and Deploy the Service

```bash
# Build the Go service
go mod tidy
go build -o extproc-service main.go

# Build Docker image
docker build -t your-registry/eag-extproc:latest .

# Push to your registry
docker push your-registry/eag-extproc:latest
```

### 2. Deploy to Kubernetes

```yaml
# deploy.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: eag-extproc
  namespace: gloo-system
spec:
  replicas: 1
  selector:
    matchLabels:
      app: eag-extproc
  template:
    metadata:
      labels:
        app: eag-extproc
    spec:
      containers:
      - name: extproc
        image: your-registry/eag-extproc:latest
        ports:
        - containerPort: 9001
---
apiVersion: v1
kind: Service
metadata:
  name: eag-extproc-service
  namespace: gloo-system
  annotations:
    gloo.solo.io/h2_service: "true"  # Enable HTTP/2 for gRPC
spec:
  selector:
    app: eag-extproc
  ports:
  - port: 9001
    targetPort: 9001
```

```bash
kubectl apply -f deploy.yaml
```

### 3. Configure Gloo Settings

Enable ExtProc globally in Gloo:

```bash
kubectl edit settings default -n gloo-system
```

Add this to the `spec` section:

```yaml
spec:
  extProc:
    grpcService:
      extProcServerRef:
        name: gloo-system-eag-extproc-service-9001
        namespace: gloo-system
    filterStage:
      stage: AuthZStage
      predicate: After
    failureModeAllow: false
    processingMode:
      requestHeaderMode: SEND
      responseHeaderMode: SKIP
```

### 4. Create a VirtualService

```yaml
# virtualservice.yaml
apiVersion: gateway.solo.io/v1
kind: VirtualService
metadata:
  name: test-api
  namespace: gloo-system
spec:
  virtualHost:
    domains:
    - "*"
    routes:
    - matchers:
      - prefix: /api
      routeAction:
        single:
          upstream:
            name: your-backend-upstream
            namespace: gloo-system
```

```bash
kubectl apply -f virtualservice.yaml
```

## Testing

### Verify the Service is Running

```bash
# Check pod status
kubectl get pods -n gloo-system | grep eag-extproc

# Check service logs
kubectl logs -n gloo-system deployment/eag-extproc -f
```

### Test Header Addition

```bash
# Make a request through Gloo
curl -v http://your-gateway-url/api/test

# Check your backend logs to see the added header:
# x-processed-by: eag-extproc
```

### Expected Output

Your backend service should receive requests with the additional header. You can verify this by:

1. Checking your backend application logs
2. Using a debug endpoint that echoes headers
3. Using httpbin for testing:

```bash
# If using httpbin as backend
curl http://your-gateway-url/api/headers

# Response will show:
{
  "headers": {
    "X-Processed-By": "eag-extproc",
    // ... other headers
  }
}
```

## Code Walkthrough

### Main Components

1. **ExtProcServer struct** - Implements the gRPC interface Gloo expects
2. **Process() method** - Handles the bidirectional stream from Gloo
3. **addProcessedByHeader() method** - Creates header modification instructions

### Key Concepts

- **Stream Processing**: ExtProc uses gRPC bidirectional streams
- **Header Mutations**: Instructions to add/remove/modify headers
- **Continue Response**: Tells Gloo to proceed with the request

### The gRPC Flow

```go
// 1. Receive request from Gloo
req, err := stream.Recv()

// 2. Check if it's request headers
if requestHeaders, ok := req.Request.(*extproc.ProcessingRequest_RequestHeaders); ok {
    // 3. Create header addition instruction
    response := s.addProcessedByHeader(requestHeaders.RequestHeaders)
    
    // 4. Send response back to Gloo
    stream.Send(response)
}
```

## Configuration Options

### Processing Modes

Configure what Gloo sends to your ExtProc service:

```yaml
processingMode:
  requestHeaderMode: SEND    # Send request headers
  requestBodyMode: SKIP      # Skip request body  
  responseHeaderMode: SKIP   # Skip response headers
  responseBodyMode: SKIP     # Skip response body
```

### Filter Placement

Control where ExtProc runs in the filter chain:

```yaml
filterStage:
  stage: AuthZStage    # Run after authentication
  predicate: After     # Run after other filters in this stage
```

### Error Handling

```yaml
failureModeAllow: false  # Fail requests if ExtProc is down
```

## Troubleshooting

### Common Issues

1. **ExtProc service not receiving requests**
    - Check if the service is running: `kubectl get pods -n gloo-system`
    - Verify gRPC service annotation: `gloo.solo.io/h2_service: "true"`
    - Check Gloo settings configuration

2. **Headers not appearing in backend**
    - Verify ExtProc is enabled in Settings
    - Check `requestHeaderMode: SEND` is set
    - Look at ExtProc service logs for processing messages

3. **503 Service Unavailable errors**
    - ExtProc service might be down or unreachable
    - Check if `failureModeAllow: false` is causing failures
    - Verify upstream configuration

### Debug Commands

```bash
# Check Gloo proxy configuration
kubectl get upstreams -n gloo-system

# View Gloo proxy logs
kubectl logs -n gloo-system deployment/gateway-proxy -f

# Check ExtProc service logs
kubectl logs -n gloo-system deployment/eag-extproc -f

# Test ExtProc service directly (advanced)
grpcurl -plaintext localhost:9001 envoy.service.ext_proc.v3.ExternalProcessor/Process
```

## Extending the Service

This minimal example can be extended to:

- **Authentication**: Validate JWT tokens or API keys
- **Rate Limiting**: Implement custom rate limiting logic
- **Request Transformation**: Modify request bodies or URLs
- **Logging/Monitoring**: Capture metrics or audit trails
- **Security**: Add security headers or content filtering

### Example Extensions

```go
// Add conditional headers based on path
if strings.Contains(requestPath, "/admin") {
    // Add admin-specific headers
}

// Remove sensitive headers
headerMutations = append(headerMutations, &extproc.HeaderMutation{
    Action: &extproc.HeaderMutation_Remove{
        Remove: "x-internal-token",
    },
})

// Reject requests based on conditions
return &extproc.ProcessingResponse{
    Response: &extproc.ProcessingResponse_ImmediateResponse{
        ImmediateResponse: &extproc.ImmediateResponse{
            Status: &extproc.HttpStatus{Code: extproc.StatusCode_Unauthorized},
            Body: `{"error": "Access denied"}`,
        },
    },
}
```

## Resources

- [Gloo Edge ExtProc Documentation](https://docs.solo.io/gloo-edge/latest/guides/traffic_management/extproc/)
- [Envoy External Processing API](https://github.com/envoyproxy/envoy/blob/main/api/envoy/service/ext_proc/v3/external_processor.proto)
- [Go Control Plane Library](https://github.com/envoyproxy/go-control-plane)

## License

This example is provided as-is for educational purposes.