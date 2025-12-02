# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Betterfly2 is a modern instant messaging platform written in Go, featuring a microservices architecture with gRPC communication, Kafka message queuing, and Redis caching. The project is a complete rewrite of the original Betterfly project using modern backend architecture patterns.

## Architecture

The system follows a microservices architecture with the following key components:

- **Data Forwarding Service**: Handles WebSocket connections and message routing between clients and backend services
- **Authentication Service**: Manages user authentication, JWT tokens, and authorization
- **Friend Service**: Handles friend relationships and contact management
- **Storage Service**: Manages message persistence with L1 (ristretto) and L2 (Redis) caching layers

## Development Setup

### Prerequisites
- Go 1.23.0+ with toolchain go1.24.1+
- Docker and Docker Compose for local development
- Protobuf compiler and Go plugins:
  ```bash
  go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
  go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
  ```

### Running Services
Use Docker Compose to start all services and dependencies:
```bash
cd services
# Set PostgreSQL DSN environment variable
export PGSQL_DSN="your_postgresql_connection_string"
docker-compose up -d
```

This starts:
- Redis (port 6379)
- Kafka cluster with 2 brokers (ports 9092, 9094)
- Kafka UI (port 8080)
- All microservices (auth, friend, data forwarding, storage)

### Building Individual Services
Each service has its own Go module. Build from the service directory:
```bash
cd services/authService
go build
```

## Code Structure

### Key Directories
- `services/` - Microservices implementation
  - `authService/` - Authentication and user management
  - `dataForwardingService/` - WebSocket gateway and message routing
  - `friendService/` - Friend relationships and contacts
  - `storageService/` - Message storage with caching layers
- `proto/` - Protocol Buffer definitions and generated code
  - `data_forwarding/` - Client-server communication protocol
  - `server_rpc/` - Inter-service gRPC definitions
- `shared/` - Common utilities and libraries
  - `logger/` - Structured logging with zap
  - `db/` - Database models and connection management
  - `utils/` - Shared utility functions

### Shared Components
- **Logger**: Uses zap for structured logging, initialized via `shared/logger/logger.go`
- **Database**: PostgreSQL with GORM ORM, connection pooling configured in `shared/db/db.go`
- **Configuration**: Environment variables for service configuration

## Communication Patterns

### Client-Server Protocol
- WebSocket connections handled by Data Forwarding Service
- Protocol Buffer messages defined in `proto/data_forwarding/`
- JWT-based authentication with tokens passed in request headers

### Inter-Service Communication
- gRPC for synchronous service-to-service calls
- Kafka for asynchronous message processing and event streaming
- Service discovery through environment variables

### Caching Strategy
- **L1 Cache**: Ristretto (in-memory, per-pod)
- **L2 Cache**: Redis (shared across pods)
- Database fallback when both caches miss

## Development Workflow

### Adding New Features
1. Define Protocol Buffer messages in `proto/` if needed
2. Generate Go code: `cd proto && make`
3. Implement service logic in respective service directory
4. Update Docker Compose configuration if adding new services

### Testing
- Services are designed to run in Docker containers
- Use the provided Docker Compose setup for integration testing
- Individual services can be built and run locally for development

### Environment Variables
Key environment variables:
- `PGSQL_DSN`: PostgreSQL connection string
- `PORT`: Service port (defaults provided)
- `REDIS_ADDR`: Redis connection address
- `KAFKA_BROKER`: Kafka broker addresses

## Dependencies

### Core Libraries
- **gRPC**: Service-to-service communication
- **GORM**: Database ORM with PostgreSQL
- **Redis**: Distributed caching
- **Kafka (Sarama)**: Message queuing
- **Zap**: Structured logging
- **Gorilla WebSocket**: Client connections

### Infrastructure
- PostgreSQL for data persistence
- Redis for caching and session storage
- Apache Kafka for message queuing
- Docker for containerization