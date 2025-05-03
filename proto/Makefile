PROTOC = ../tool/bin/protoc
PROTO_DIR = .
PROTOC_GEN_GO = ../tool/bin/protoc-gen-go
PROTOC_GEN_GO_GRPC = ../tool/bin/protoc-gen-go-grpc

# 找到所有proto文件
PROTO_FILES = $(shell find $(PROTO_DIR) -name '*.proto')

all: gen

gen:
	@echo "正在从proto文件生成Go文件..."
	@for file in $(PROTO_FILES); do \
		$(PROTOC) \
			--plugin=protoc-gen-go=$(PROTOC_GEN_GO) \
			--plugin=protoc-gen-go-grpc=$(PROTOC_GEN_GO_GRPC) \
			-I=. \
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