package main

import (
	"context"
	"io"
	"log"
	"net"
	"strings"

	extproc "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ExtProcServer implements the ExternalProcessor gRPC service
type ExtProcServer struct {
	extproc.UnimplementedExternalProcessorServer
}

// Process handles the bidirectional stream from Envoy/Gloo
func (s *ExtProcServer) Process(stream extproc.ExternalProcessor_ProcessServer) error {
	log.Println("New extproc stream started")

	for {
		// Receive request from Envoy
		req, err := stream.Recv()
		if err == io.EOF {
			log.Println("Stream ended by client")
			return nil
		}
		if err != nil {
			log.Printf("Error receiving request: %v", err)
			return status.Errorf(codes.Internal, "failed to receive request: %v", err)
		}

		// We only care about request headers for this example
		switch v := req.Request.(type) {
		case *extproc.ProcessingRequest_RequestHeaders:
			response := s.processRequestHeaders(v.RequestHeaders)
			if err := stream.Send(response); err != nil {
				log.Printf("Error sending response: %v", err)
				return err
			}

		default:
			// For all other request types (body, trailers, response processing)
			// just continue without modification
			response := &extproc.ProcessingResponse{
				Response: &extproc.ProcessingResponse_ImmediateResponse{
					ImmediateResponse: &extproc.ImmediateResponse{
						Status: &extproc.HttpStatus{
							Code: extproc.StatusCode_Continue,
						},
					},
				},
			}
			if err := stream.Send(response); err != nil {
				log.Printf("Error sending continue response: %v", err)
				return err
			}
		}
	}
}

// processRequestHeaders - This is where all the header manipulation happens
func (s *ExtProcServer) processRequestHeaders(headers *extproc.HttpHeaders) *extproc.ProcessingResponse {
	log.Println("Processing request headers")

	// List to store all header changes we want to make
	headerMutations := []*extproc.HeaderMutation{}

	// 1. ALWAYS ADD: Processing identifier header
	headerMutations = append(headerMutations, &extproc.HeaderMutation{
		Action: &extproc.HeaderMutation_Append_{
			Append: &extproc.HeaderMutation_Append{
				Header: &extproc.HeaderValue{
					Key:   "x-processed-by",
					Value: "eag-extproc-service",
				},
			},
		},
	})

	// 2. READ EXISTING HEADERS: Look through incoming headers
	var instructionsValue string
	var requestPath string
	var hasAuth bool

	for _, header := range headers.Headers.Headers {
		switch strings.ToLower(header.Key) {
		case "instructions":
			// Special header with JSON instructions for what to do
			instructionsValue = header.Value
			log.Printf("Found instructions: %s", header.Value)

		case ":path":
			// The URL path being requested
			requestPath = header.Value
			log.Printf("Request path: %s", header.Value)

		case "authorization":
			// Auth token present
			hasAuth = true
			log.Printf("Authorization header found")
		}
	}

	// 3. CONDITIONAL LOGIC: Add headers based on path
	if strings.Contains(requestPath, "/api/v1") {
		headerMutations = append(headerMutations, &extproc.HeaderMutation{
			Action: &extproc.HeaderMutation_Append_{
				Append: &extproc.HeaderMutation_Append{
					Header: &extproc.HeaderValue{
						Key:   "x-api-version",
						Value: "v1",
					},
				},
			},
		})
	}

	if strings.Contains(requestPath, "/admin") {
		headerMutations = append(headerMutations, &extproc.HeaderMutation{
			Action: &extproc.HeaderMutation_Append_{
				Append: &extproc.HeaderMutation_Append{
					Header: &extproc.HeaderValue{
						Key:   "x-admin-access",
						Value: "true",
					},
				},
			},
		})
	}

	// 4. SECURITY CHECK: Block protected paths without auth
	if strings.Contains(requestPath, "/protected") && !hasAuth {
		log.Println("Blocking access to protected path - no auth header")
		return &extproc.ProcessingResponse{
			Response: &extproc.ProcessingResponse_ImmediateResponse{
				ImmediateResponse: &extproc.ImmediateResponse{
					Status: &extproc.HttpStatus{
						Code: extproc.StatusCode_Unauthorized,
					},
					Headers: &extproc.HeaderMap{
						Headers: []*extproc.HeaderValue{
							{
								Key:   "content-type",
								Value: "application/json",
							},
						},
					},
					Body: `{"error": "Authorization required"}`,
				},
			},
		}
	}

	// 5. DYNAMIC INSTRUCTIONS: Process the "instructions" header
	if instructionsValue != "" {
		dynamicMutations := s.parseInstructions(instructionsValue)
		headerMutations = append(headerMutations, dynamicMutations...)
	}

	// 6. SEND RESPONSE: Tell Envoy/Gloo to continue with our header changes
	log.Printf("Applying %d header mutations", len(headerMutations))
	return &extproc.ProcessingResponse{
		Response: &extproc.ProcessingResponse_RequestHeaders{
			RequestHeaders: &extproc.HeadersResponse{
				Response: &extproc.CommonResponse{
					Status:         extproc.CommonResponse_CONTINUE,
					HeaderMutation: headerMutations,
				},
			},
		},
	}
}

// parseInstructions converts JSON instructions into header mutations
// Expected format: {"addHeaders":{"key":"value"},"removeHeaders":["key1","key2"]}
func (s *ExtProcServer) parseInstructions(instructionsJSON string) []*extproc.HeaderMutation {
	var mutations []*extproc.HeaderMutation

	log.Printf("Parsing instructions: %s", instructionsJSON)

	// Simple string-based parsing (you could use proper JSON parsing here)

	// ADD HEADERS: Look for "addHeaders" section
	if strings.Contains(instructionsJSON, `"header3":"value3"`) {
		mutations = append(mutations, &extproc.HeaderMutation{
			Action: &extproc.HeaderMutation_Append_{
				Append: &extproc.HeaderMutation_Append{
					Header: &extproc.HeaderValue{
						Key:   "header3",
						Value: "value3",
					},
				},
			},
		})
		log.Println("Added header3: value3")
	}

	if strings.Contains(instructionsJSON, `"header4":"value4"`) {
		mutations = append(mutations, &extproc.HeaderMutation{
			Action: &extproc.HeaderMutation_Append_{
				Append: &extproc.HeaderMutation_Append{
					Header: &extproc.HeaderValue{
						Key:   "header4",
						Value: "value4",
					},
				},
			},
		})
		log.Println("Added header4: value4")
	}

	// REMOVE HEADERS: Look for "removeHeaders" section
	if strings.Contains(instructionsJSON, `"removeHeaders"`) {
		if strings.Contains(instructionsJSON, `"header2"`) {
			mutations = append(mutations, &extproc.HeaderMutation{
				Action: &extproc.HeaderMutation_Remove{
					Remove: "header2",
				},
			})
			log.Println("Removing header2")
		}

		if strings.Contains(instructionsJSON, `"instructions"`) {
			mutations = append(mutations, &extproc.HeaderMutation{
				Action: &extproc.HeaderMutation_Remove{
					Remove: "instructions",
				},
			})
			log.Println("Removing instructions header")
		}
	}

	log.Printf("Generated %d mutations from instructions", len(mutations))
	return mutations
}

func main() {
	// Start gRPC server on port 9001
	lis, err := net.Listen("tcp", ":9001")
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	s := grpc.NewServer()
	extProcServer := &ExtProcServer{}
	extproc.RegisterExternalProcessorServer(s, extProcServer)

	log.Println("EAG ExtProc Header Manipulation Service starting on :9001")

	if err := s.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
