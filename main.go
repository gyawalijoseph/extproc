package main

import (
	"io"
	"log"
	"net"
	"net/http"

	// Import the Envoy external processor gRPC definitions
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"

	// Health check imports - these provide ready-to-use health check implementations

	"github.com/solo-io/go-utils/healthchecker"             // Solo.io's health checker utility
	grpchealth "google.golang.org/grpc/health"              // gRPC health service implementation
	healthpb "google.golang.org/grpc/health/grpc_health_v1" // gRPC health service protocol definitions
)

// ExtProcServer implements the ExternalProcessor interface that Gloo expects
type ExtProcServer struct {
	extprocv3.UnimplementedExternalProcessorServer
}

// Process is the main function that Gloo calls for every HTTP request
// It receives a bidirectional stream where Gloo sends request data
// and we send back instructions on what to modify
func (s *ExtProcServer) Process(stream extprocv3.ExternalProcessor_ProcessServer) error {
	log.Println("New HTTP request received from Gloo")

	// Keep listening for messages from Gloo on this stream
	for {
		// Receive the next message from Gloo (could be headers, body, etc.)
		req, err := stream.Recv()
		if err == io.EOF {
			// Stream ended normally
			log.Println("Stream ended")
			return nil
		}
		if err != nil {
			// Something went wrong
			log.Printf("Error receiving from Gloo: %v", err)
			return err
		}

		// Check if this message contains request headers
		// We only care about headers, ignore body/trailers/etc.
		if requestHeaders, ok := req.Request.(*extprocv3.ProcessingRequest_RequestHeaders); ok {
			log.Println("Processing request headers")

			// Add our header and send response back to Gloo
			response := s.addProcessedByHeader(requestHeaders.RequestHeaders)
			err := stream.Send(response)
			if err != nil {
				log.Printf("Error sending response to Gloo: %v", err)
				return err
			}

			log.Println("Header added and response sent to Gloo")
		}
		// For any other message types (body, trailers, response headers, etc.)
		// we just ignore them - Gloo will process them normally
	}
}

// addProcessedByHeader takes the incoming headers and returns instructions
// to add our custom "x-processed-by" header
func (s *ExtProcServer) addProcessedByHeader(headers *extprocv3.HttpHeaders) *extprocv3.ProcessingResponse {
	log.Println("Creating header mutation to add x-processed-by header")

	headerMutation := &extprocv3.HeaderMutation{
		SetHeaders: []*corev3.HeaderValueOption{
			{
				Header: &corev3.HeaderValue{
					Key:   "x-processed-by",
					Value: "ext-proc-go-server",
				},
				AppendAction: corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD,
			},
		},
	}

	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_RequestHeaders{
			RequestHeaders: &extprocv3.HeadersResponse{
				Response: &extprocv3.CommonResponse{
					HeaderMutation: headerMutation,
				},
			},
		},
	}
}

func main() {
	log.Println("Starting EAG ExtProc service...")

	// Start HTTP health check server in a separate goroutine
	// This provides a simple HTTP endpoint that Kubernetes can use for health checks
	go func() {
		// Create HTTP server for health checks on port 8080
		http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
		})

		// Start HTTP server for health checks
		log.Println("Health check server starting on :8080/health")
		if err := http.ListenAndServe(":8080", nil); err != nil {
			log.Printf("Health check server failed: %v", err)
		}
	}()

	// Create a TCP listener on port 9001 (standard ExtProc port)
	lis, err := net.Listen("tcp", ":9001")
	if err != nil {
		log.Fatalf("Failed to create listener on port 9001: %v", err)
	}
	log.Println("gRPC server listening on port 9001")

	// Create a new gRPC server
	grpcServer := grpc.NewServer()
	log.Println("gRPC server created")

	// Register our ExtProc service with the gRPC server
	// This tells gRPC that our ExtProcServer should handle ExtProc requests
	extprocv3.RegisterExternalProcessorServer(grpcServer, &ExtProcServer{})
	log.Println("ExtProc service registered")

	// Set up gRPC health checking
	healthServer := grpchealth.NewServer()
	hc := healthchecker.NewGrpc("ext-proc", healthServer, false, healthpb.HealthCheckResponse_SERVING)
	// Register the health server with gRPC
	healthpb.RegisterHealthServer(grpcServer, hc.GetServer())

	log.Println("gRPC health service registered and set to SERVING")

	log.Println("Service will add header: x-processed-by: eag-extproc")
	log.Println("Health check endpoints:")
	log.Println("  - HTTP: http://localhost:8080/health")
	log.Println("  - gRPC: grpc://localhost:9001 (health service)")
	log.Println("Ready to receive requests from Gloo Gateway...")

	// Start the gRPC server and block here
	// Gloo will connect to this server and send HTTP request data
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Failed to start gRPC server: %v", err)
	}
}
