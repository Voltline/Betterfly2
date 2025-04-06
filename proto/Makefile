PROTO_DIR=.
OUT_DIR=gen
PROTOC_GEN_GO=protoc-gen-go
PROTOC_GEN_GO_GRPC=protoc-gen-go-grpc

PROTO_FILES=$(shell find $(PROTO_DIR) -name '*.proto')

all: gen

gen:
	@echo "正在从proto文件生成Go文件"
	@protoc \
		--proto_path=$(PROTO_DIR) \
		--go_out=$(OUT_DIR) --go_opt=paths=source_relative \
		--go-grpc_out=$(OUT_DIR) --go-grpc_opt=paths=source_relative \
		$(PROTO_FILES)

clean:
	@echo "正在清理生成文件"
	@rm -rf $(OUT_DIR)

.PHONY: all gen clean