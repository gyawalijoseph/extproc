package main

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"

	// Import the Envoy external processor gRPC definitions
	extproc "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"

	// Health check imports - these provide ready-to-use health check implementations
	"github.com/solo-io/go-utils/healthchecker"             // Solo.io's health checker utility
	grpchealth "google.golang.org/grpc/health"              // gRPC health service implementation
	healthpb "google.golang.org/grpc/health/grpc_health_v1" // gRPC health service protocol definitions
)

// ExtProcServer implements the ExternalProcessor interface that Gloo expects
type ExtProcServer struct {
	extproc.UnimplementedExternalProcessorServer
}

// Process is the main function that Gloo calls for every HTTP request
// It receives a bidirectional stream where Gloo sends request data
// and we send back instructions on what to modify
func (s *ExtProcServer) Process(stream extproc.ExternalProcessor_ProcessServer) error {
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
		if requestHeaders, ok := req.Request.(*extproc.ProcessingRequest_RequestHeaders); ok {
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
func (s *ExtProcServer) addProcessedByHeader(headers *extproc.HttpHeaders) *extproc.ProcessingResponse {
	log.Println("Creating header mutation to add x-processed-by header")

	// Create a single header mutation that tells Gloo to add our header
	headerMutation := &extproc.HeaderMutation{
		Action: &extproc.HeaderMutation_Append_{
			Append: &extproc.HeaderMutation_Append{
				Header: &extproc.HeaderValue{
					Key:   "x-processed-by", // Header name
					Value: "eag-extproc",    // Header value
				},
			},
		},
	}

	// Package the header mutation into a response that tells Gloo:
	// "Continue processing this request, but first add this header"
	return &extproc.ProcessingResponse{
		Response: &extproc.ProcessingResponse_RequestHeaders{
			RequestHeaders: &extproc.HeadersResponse{
				Response: &extproc.CommonResponse{
					Status:         extproc.CommonResponse_CONTINUE,           // Keep processing the request
					HeaderMutation: []*extproc.HeaderMutation{headerMutation}, // Add our header
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
	extproc.RegisterExternalProcessorServer(grpcServer, &ExtProcServer{})
	log.Println("ExtProc service registered")

	// Register gRPC health service - this allows gRPC health checks
	// Kubernetes can use this to check if the gRPC service is healthy
	healthServer := grpchealth.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthServer)

	// Set the ExtProc service as healthy
	healthServer.SetServingStatus("envoy.service.ext_proc.v3.ExternalProcessor", healthpb.HealthCheckResponse_SERVING)
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING) // Overall server health
	log.Println("gRPC health service registered and set to SERVING")

	// Start healthchecker utility (Solo.io's health checker)
	// This provides additional health monitoring capabilities
	hc := healthchecker.NewGrpc("ExtProc Service", grpcServer)
	hc.StartServer(context.Background())
	log.Println("Solo.io health checker started")

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
