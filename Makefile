.PHONY: build lamoid 

WEAVIATE_VERSION := v1.24.9
OLLAMA_VERSION := v0.1.32
DIST_DIR := ./dist
TMP_DIR := /tmp/weaviate-installation
ZIP_FILE := weaviate-$(WEAVIATE_VERSION)-darwin-all.zip
OLLAMA_BIN := ollama-darwin
OLLAMA_URL := https://github.com/ollama/ollama/releases/download/$(OLLAMA_VERSION)/$(OLLAMA_BIN)

PACKAGE := main

include .build.env

LDFLAGS := -X "$(PACKAGE).PosthogAPIKey=$(POSTHOG_PERSONAL_API_KEY)"

all: macapp

dist/ollama:
	# Ensure the distribution directory exists
	mkdir -p $(DIST_DIR)

	# Download the Ollama binary from GitHub
	curl -L $(OLLAMA_URL) -o $(DIST_DIR)/ollama

	# Make the binary executable
	chmod +x $(DIST_DIR)/ollama

dist/weaviate:
	# Ensure dist directory exists
	mkdir -p $(DIST_DIR)

	# Create a temporary directory for installation
	mkdir -p $(TMP_DIR)

	# Download the Weaviate zip file into the temporary directory
	curl -L https://github.com/weaviate/weaviate/releases/download/$(WEAVIATE_VERSION)/$(ZIP_FILE) -o $(TMP_DIR)/$(ZIP_FILE)

	# Unzip the downloaded file
	unzip $(TMP_DIR)/$(ZIP_FILE) -d $(TMP_DIR)

	# Move the weaviate binary to the dist directory
	mv $(TMP_DIR)/weaviate $(DIST_DIR)/weaviate

	# Remove the temporary directory and the zip file
	rm -rf $(TMP_DIR)

lamoid:
	# Ensure dist directory exists
	mkdir -p $(DIST_DIR)
	# Modelfile is needed for any custom model execution
	cp Modelfile.* dist/

	echo "$(LDFLAGS)"


	pushd lamoid && go build -ldflags="$(LDFLAGS)" -o ../$(DIST_DIR)/lamoid . && popd

macapp: lamoid dist/ollama dist/weaviate
	pushd macapp && npm install && npm run package && popd