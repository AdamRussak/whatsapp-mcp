# Use a base image with both Go and Python
FROM golang:1.24-bullseye as builder

# Set up Go environment
ENV CGO_ENABLED=1
ENV GO111MODULE=on

# Create working directory
WORKDIR /app

# Copy the go bridge project files
COPY . .

# Build the Go WhatsApp bridge
WORKDIR /app/whatsapp-bridge
RUN go mod download
RUN go build -o whatsapp-bridge

FROM python:3.13 as runtime

WORKDIR /app

# Copy the Python Gradio project files
COPY  --from=builder /app/whatsapp-mcp-server /app/whatsapp-mcp-server

# Copy the GO exec from the previus stage
RUN mkdir -p /app/whatsapp-bridge
COPY --from=builder /app/whatsapp-bridge/whatsapp-bridge /app/whatsapp-bridge/whatsapp-bridge
RUN chmod +x /app/whatsapp-bridge/whatsapp-bridge

# Install Python and other dependencies
RUN apt-get update 
RUN apt-get install -y ffmpeg \
    sqlite3 \
    && rm -rf /var/lib/apt/lists/*

# Set up Python environment
RUN python3 -m pip install --upgrade pip
RUN python3 -m pip install uv

# Install dependencies from requirements.txt
WORKDIR /app/whatsapp-mcp-server
COPY --from=builder /app/whatsapp-mcp-server/requirements.txt /app/whatsapp-mcp-server/requirements.txt
RUN pip install -r requirements.txt

# Set up Python MCP server
WORKDIR /app/whatsapp-mcp-server

# Create directories for persistent storage
RUN mkdir -p /app/store

# Set up entrypoint script
COPY entrypoint.sh /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh

# Expose ports for MCP server and Gradio UI
EXPOSE 8081
EXPOSE 8082
ENTRYPOINT ["/app/entrypoint.sh"]