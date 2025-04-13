PROTOC=protoc
PROTO_DIR=.
OUT_DIR=gen
PROTOC_GEN_GO=protoc-gen-go
PROTOC_GEN_GO_GRPC=protoc-gen-go-grpc

PROTO_FILES=$(shell find $(PROTO_DIR) -name '*.proto')

all: gen

gen:
	@echo "正在从proto文件生成Go文件..."
	@for file in $(PROTO_FILES); do \
		$(PROTOC) \
			-I=. \
			--proto_path=$(PROTO_DIR) \
			--go_out=. --go_opt=paths=source_relative \
			--go-grpc_out=. --go-grpc_opt=paths=source_relative \
			$$file; \
	done
	@echo "proto编译完成"

clean:
	@echo "正在清理生成文件..."
	@find $(PROTO_DIR) -name '*.pb.go' -delete
	@find $(PROTO_DIR) -name '*.pb.grpc.go' -delete
	@echo "清理完成"

.PHONY: all gen clean
