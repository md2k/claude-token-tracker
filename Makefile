.PHONY: help build install clean

# Default target
help:
	@echo "Claude Token Counter - Build & Install"
	@echo ""
	@echo "Available targets:"
	@echo "  make build    - Build the token-tracker binary"
	@echo "  make install  - Build and install to ~/.claude (if directory exists)"
	@echo "  make clean    - Remove built binary"
	@echo "  make help     - Show this help message"

# Build the binary
build:
	@echo "Building token-tracker for current OS..."
	go build -o token-tracker main.go
	@echo "✓ Built successfully: token-tracker"

# Build and install to ~/.claude if it exists
install: build
	@if [ -d "$$HOME/.claude" ]; then \
		echo "Installing to ~/.claude..."; \
		cp token-tracker "$$HOME/.claude/"; \
		cp statusline.mjs "$$HOME/.claude/"; \
		chmod +x "$$HOME/.claude/statusline.mjs"; \
		chmod +x "$$HOME/.claude/token-tracker"; \
		echo "✓ Installed to ~/.claude/"; \
		echo ""; \
		echo "Next steps:"; \
		echo "  1. Update ~/.claude/settings.json with:"; \
		echo '     "statusLine": { "type": "command", "command": "~/.claude/statusline.mjs" }'; \
		echo "  2. Restart Claude Code"; \
	else \
		echo "⚠ ~/.claude directory not found"; \
		echo "  Binary built successfully but not installed"; \
		echo "  Create ~/.claude first or copy files manually"; \
	fi

# Clean built artifacts
clean:
	@echo "Cleaning built files..."
	@rm -f token-tracker
	@echo "✓ Cleaned"
