package main

import (
	"io"
	"log"
	"net"

	// Import the Envoy external processor gRPC definitions
	extproc "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"
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

	// Create a TCP listener on port 9001 (standard ExtProc port)
	lis, err := net.Listen("tcp", ":9001")
	if err != nil {
		log.Fatalf("Failed to create listener on port 9001: %v", err)
	}
	log.Println("Listening on port 9001")

	// Create a new gRPC server
	grpcServer := grpc.NewServer()
	log.Println("gRPC server created")

	// Register our ExtProc service with the gRPC server
	// This tells gRPC that our ExtProcServer should handle ExtProc requests
	extproc.RegisterExternalProcessorServer(grpcServer, &ExtProcServer{})
	log.Println("ExtProc service registered")

	log.Println("Service will add header: x-processed-by: eag-extproc")
	log.Println("Ready to receive requests from Gloo Gateway...")

	// Start the gRPC server and block here
	// Gloo will connect to this server and send HTTP request data
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Failed to start gRPC server: %v", err)
	}
}
