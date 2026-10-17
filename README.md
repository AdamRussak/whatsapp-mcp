# WhatsApp MCP Server - Enhanced Enterprise Edition

A comprehensive Model Context Protocol (MCP) server for WhatsApp with advanced webhook capabilities, contact management, and enterprise-grade features.

This enhanced version provides **complete WhatsApp integration** with your personal account, featuring advanced contact management, real-time webhooks, comprehensive media handling, and a modern web interface for webhook management.

Built with a containerized microservices architecture, this system offers professional-grade reliability, scalability, and maintainability for enterprise and developer use cases.

![WhatsApp MCP](./example-use.png)

## 🚀 Key Features

### 📱 Core WhatsApp Integration
- **Direct Personal Account Connection**: Connect to your personal WhatsApp account via the official multidevice API
- **Complete Message History**: Full access to your message history with advanced search and filtering
- **Media Support**: Send and receive images, videos, documents, and audio messages
- **Voice Messages**: Send audio files as playable WhatsApp voice messages with automatic format conversion
- **Group Chat Support**: Full support for group chats with participant management
- **Real-time Synchronization**: Instant message synchronization with your WhatsApp account

### 🔗 Advanced Webhook System
- **Real-time Notifications**: Instant HTTP webhook notifications for incoming messages
- **Flexible Trigger System**: Configure webhooks based on sender, keywords, media types, or all messages
- **Pattern Matching**: Support for exact match, contains, and regex pattern matching
- **Retry Logic**: Exponential backoff retry mechanism with comprehensive logging
- **Security**: HMAC-SHA256 signature authentication for webhook endpoints
- **Auto-Download**: A listener can capture matched voice notes to disk as they arrive, before WhatsApp's CDN links expire (~11–19 days)
- **LID-Aware Matching**: Trigger values given as phone numbers also match the `@lid` chats WhatsApp now uses for 1:1 conversations
- **Web UI**: Modern responsive web interface for webhook management

### 👥 Enhanced Contact Management
- **Unified Contact Resolution**: Intelligent contact name resolution from multiple sources
- **Custom Nicknames**: Set and manage custom nicknames for contacts
- **Rich Contact Data**: Access to full contact information including business names and profile data
- **Advanced Search**: Search contacts by name, phone number, or custom attributes
- **Contact Prioritization**: Smart name resolution with customizable priority ordering

### 🏗️ Enterprise Architecture
- **Microservices Design**: Containerized architecture with separate services for different functions
- **Persistent Storage**: Reliable SQLite database storage with shared volumes
- **Scalable Deployment**: Docker Compose setup for easy deployment and scaling

## 🏛️ System Architecture

This application consists of three main containerized services:

### 1. **WhatsApp Bridge Service** (`whatsapp-bridge/`)
- **Technology**: Go application using whatsmeow library
- **Port**: 8080
- **Responsibilities**: 
  - WhatsApp API connection and authentication
  - Message processing and storage
  - Webhook delivery and management
  - RESTful API endpoints
  - Contact and chat management

### 2. **MCP Server Service** (`whatsapp-mcp-server/`)
- **Technology**: Python with Model Context Protocol
- **Ports**: 8081 (MCP), 8082 (Gradio UI)
- **Responsibilities**:
  - MCP protocol implementation for Claude/AI integration
  - Advanced message search and filtering
  - Media download and processing
  - Contact management tools
  - Gradio web interface for testing

### 3. **Webhook Management UI** (`whatsapp-webhook-ui/`)
- **Technology**: Modern HTML/CSS/JavaScript SPA
- **Port**: 8089
- **Responsibilities**:
  - Webhook configuration management
  - Real-time webhook testing
  - Delivery log monitoring
  - Responsive web interface

## 📦 Installation

### Prerequisites

- **Docker & Docker Compose**: Latest versions
- **Python 3.11+**: For MCP server
- **Go 1.24+**: For WhatsApp bridge
- **UV Package Manager**: Install with `curl -LsSf https://astral.sh/uv/install.sh | sh`
- **FFmpeg** (optional): For audio message conversion
- **Anthropic Claude Desktop** or **Cursor**: For AI integration

### Quick Start with Docker

1. **Clone the repository**

   ```bash
   git clone https://github.com/AdamRussak/whatsapp-mcp
   cd whatsapp-mcp
   ```

2. **Start all services**

   ```bash
   docker network create n8n_n8n_traefik_network
   docker-compose up -d
   ```

   This will start:
   - WhatsApp Bridge: `http://localhost:8080`
   - MCP Server: `http://localhost:8081`
   - Gradio UI: `http://localhost:8082`
   - Webhook Manager: `http://localhost:8089`

3. **Initial Setup**

   On first run, you'll need to scan a QR code to authenticate with WhatsApp:
   
   ```bash
   # Watch the bridge logs for QR code
   docker-compose logs -f whatsapp-bridge
   ```

   Scan the QR code with your WhatsApp mobile app to authenticate.

### Manual Development Setup

1. **Start WhatsApp Bridge**

   ```bash
   cd whatsapp-bridge
   go run main.go
   ```

2. **Start MCP Server**

   ```bash
   cd whatsapp-mcp-server
   uv sync
   uv run python whatsapp.py
   ```

3. **Start Webhook UI**

   ```bash
   cd whatsapp-webhook-ui
   python3 -m http.server 8089
   ```

## 🛠️ Advanced Features

### Webhook System

A webhook entry is a **listener**: its triggers decide which messages it matches, and its
actions decide what happens to them. Posting to a URL is one action; auto-downloading the
matched audio is another. An entry may do either or both.

#### Configuration Options
- **Trigger Types**: all, chat_jid, sender, keyword, media_type
- **Match Types**: exact, contains, regex
- **Delivery**: Asynchronous with exponential backoff
- **Security**: HMAC-SHA256 signature authentication
- **Logging**: Delivery *and* capture outcomes, per listener
- **`auto_download`** (bool): download matched audio as it arrives
- **`auto_download_voice_only`** (bool, default `true`): voice notes only; `false` also takes forwarded music and attached audio files
- **`webhook_url`**: required *unless* `auto_download` is on — a listener with auto-download and no URL captures media and posts nowhere

> **Phone numbers vs LID.** WhatsApp moved 1:1 chats to LID addressing, so a contact saved as
> `972547669192` now sends from something like `62156783493249@lid`. Exact `sender` and
> `chat_jid` trigger values are expanded through WhatsApp's LID map when configs load, so
> typing the phone number works. `contains` and `regex` values are left exactly as written.

#### Example: capture a contact's voice notes (no HTTP delivery)
```json
{
  "name": "Capture voice notes",
  "webhook_url": "",
  "enabled": true,
  "auto_download": true,
  "auto_download_voice_only": true,
  "triggers": [
    { "trigger_type": "sender", "trigger_value": "972547669192", "match_type": "exact", "enabled": true }
  ]
}
```
Files land in `store/media/<chat>/` and each capture is recorded in that listener's logs.

#### Example Webhook Configuration
```json
{
  "name": "Urgent Messages",
  "webhook_url": "https://your-system.com/webhook",
  "secret_token": "your-secret-key",
  "enabled": true,
  "triggers": [
    {
      "trigger_type": "keyword",
      "trigger_value": "urgent|emergency|help",
      "match_type": "regex",
      "enabled": true
    }
  ]
}
```

### Contact Management

#### Advanced Features
- **Custom Nicknames**: Set personalized names for contacts
- **Multi-source Resolution**: Combines WhatsApp contacts, chat history, and custom data
- **Smart Prioritization**: Intelligent name resolution with customizable priority
- **Bulk Operations**: Mass contact management and updates

#### Priority Order
1. Custom Nickname (highest priority)
2. Full Name (from WhatsApp contacts)
3. Push Name (WhatsApp display name)
4. First Name (from WhatsApp contacts)
5. Business Name (for business contacts)
6. Phone Number (fallback)

### Media Handling

#### Supported Media Types
- **Images**: JPEG, PNG, GIF, WebP
- **Videos**: MP4, AVI, MOV, WebM
- **Audio**: MP3, WAV, OGG, M4A (auto-converted to voice messages)
- **Documents**: PDF, DOC, DOCX, XLS, XLSX, PPT, PPTX

#### Features
- **Automatic Conversion**: Audio files converted to WhatsApp voice message format
- **Media Download**: Direct download of received media files
- **Metadata Extraction**: Comprehensive media information and thumbnails
- **Size Optimization**: Automatic compression for large files

#### Voice Note Auto-Capture
WhatsApp CDN links for media expire after roughly 11–19 days, after which a voice note
cannot be recovered — a media retry returns a copy encrypted under a key the bridge never
receives. A listener with `auto_download` on closes that window by downloading matched audio
within seconds of arrival.

- Capture happens **after** the message is stored and **never** inside the WhatsApp event
  handler: matches are queued to a small worker pool (2 by default), and a full queue drops
  the job with a warning rather than delaying message handling.
- Captures skip media retry entirely, so no retry receipt is ever sent to anyone's phone for
  live media.
- Outcomes are written to that listener's logs (`event_type: auto_download`) and to
  `store/media.db`. Files are named `<date>_<me|them>_<message id>.ogg`.
- View-once audio is never captured: no key or URL is stored for it by design.

#### Retention
A listener can keep its captures for a set number of days ("Keep captured files for" in
the UI, `retention_days` in the API; blank or 0 keeps them forever). Retention is the only
code that deletes files, so it keeps a file whenever anything is unclear:

- It only removes files a listener **captured**. Files you fetched by hand
  (`POST /api/download`, batch jobs) are never touched — nor anything captured before
  capture logging existed.
- A file goes only when **every** listener that captured it has an expired retention; one
  listener that keeps forever, is disabled, or has not yet expired keeps it.
- Only `.ogg`/`.mp3` files under `store/media/` can be removed, never through a symlink,
  at most 500 per sweep. Each removal appears as `REMOVED` in that listener's logs, and the
  record becomes `pruned`, so no batch run downloads it again (`retry_failed` or
  `"force": true` re-fetches on purpose).
- It ships in **dry run**. Check `GET /api/retention/preview`, then set
  `RETENTION_DRY_RUN=false` and redeploy.

To see what retention would do on a later date, against a copy of the store:
```bash
STORE_DIR=../store-test go run ./cmd/retention -at 2026-12-01          # read-only
touch ../store-test/REHEARSAL                                            # opt in to deleting
STORE_DIR=../store-test go run ./cmd/retention -at 2026-12-01 -apply    # on the copy only
```

## 🔧 MCP Tools Reference

### Core Messaging Tools
- **`send_message`**: Send text messages to contacts or groups
- **`send_file`**: Send media files with automatic type detection
- **`send_audio_message`**: Send audio as WhatsApp voice messages
- **`download_media`**: Download received media files

### Search and Discovery
- **`search_contacts`**: Advanced contact search with multiple criteria
- **`list_messages`**: Retrieve messages with filtering and pagination
- **`list_chats`**: Get chat list with metadata and last message info
- **`get_message_context`**: Get conversation context around specific messages

### Contact Management
- **`get_contact_details`**: Get comprehensive contact information
- **`list_all_contacts`**: List all contacts with rich metadata
- **`set_contact_nickname`**: Set custom nicknames for contacts
- **`get_contact_nickname`**: Retrieve custom nicknames
- **`remove_contact_nickname`**: Remove custom nicknames
- **`list_contact_nicknames`**: List all custom nicknames

### Chat Operations
- **`get_chat`**: Get detailed chat information
- **`get_direct_chat_by_contact`**: Find direct chat with specific contact
- **`get_contact_chats`**: List all chats involving a contact
- **`get_last_interaction`**: Get most recent interaction with contact

## 📊 API Reference

### WhatsApp Bridge API (`localhost:8080/api`)

#### Message Operations
```bash
# Send message
POST /api/send
Content-Type: application/json

{
  "recipient": "1234567890@s.whatsapp.net",
  "message": "Hello World",
  "media_path": "/path/to/file.jpg"
}

# Download one message's audio, image or video (writes to store/media/<chat>/).
# Documents are not downloadable. Media older than ~11-19 days is usually gone from the CDN.
POST /api/download
Content-Type: application/json

{ "message_id": "MESSAGE_ID", "chat_jid": "CHAT_JID" }

# Download a chat's audio history as a background job
POST /api/download/batch
{ "chat": "972547669192", "media_type": "voice", "limit": 500, "skip_cached": true }

GET    /api/download/batch/{job_id}     # progress
DELETE /api/download/batch/{job_id}     # cancel a running job

# Stream a downloaded file (range requests supported; .ogg .mp3 .jpg .jpeg .png .webp .gif
# .mp4 only; ?download=1 to save)
GET /api/media/{chat}/{filename}

# Downloaded files with the message behind each (chat, after, before, status, limit, offset)
GET /api/downloads

# Chats for pickers, searchable by name or phone number (q, type=all|contact|group, limit)
GET /api/chats

# Ask the phone for older history in one chat; results arrive through the normal sync (chat, count)
GET /api/backfill?chat=1234567890@s.whatsapp.net&count=200

# What the next retention sweep would remove — read-only
GET /api/retention/preview

# Schema version, WhatsApp connection, auto-download pool and retention mode
GET /api/health
```

A download removed by retention returns **410 Gone** from `POST /api/download` unless the
body sets `"force": true`.

WhatsApp only sends a limited window of past messages when the bridge first connects, so
older conversations can look thin. `GET /api/backfill` asks the phone for messages older than
the oldest one already stored for that chat; the reply comes back as a normal history sync and
is written to the same database, so allow a few seconds before reading. The phone has to be
online. `scripts/backfill.py <YYYY-MM-DD> <chat_jid> [...]` repeats the request until a chat
reaches the date or the phone stops returning anything older.

#### Webhook Management
```bash
# List webhooks
GET /api/webhooks

# Create webhook
POST /api/webhooks
{
  "name": "My Webhook",
  "webhook_url": "https://example.com/webhook",
  "secret_token": "optional-secret",
  "enabled": true,
  "trigger_mode": "or",   # or "and": every enabled trigger must match
  "triggers": [...]
}

# Check a listener without saving it: the same 400 a save would give, listing every problem
POST /api/webhooks/validate

# Test webhook
POST /api/webhooks/{id}/test

# View one listener's logs — deliveries and captures
GET /api/webhooks/{id}/logs

# All logs
GET /api/webhook-logs
```

Log entries carry `event_type`: `delivery` for an HTTP post, `auto_download` for a capture.
A capture row has no status code — `delivered_at` is set when the file landed, and
`response_body` holds the saved path.

## 🔐 Security Features

### Authentication
- **WhatsApp Authentication**: Secure QR code pairing with your personal account
- **Session Management**: Persistent session storage with automatic reconnection
- **API Security**: Rate limiting and request validation

### Webhook Security
- **HMAC Signatures**: SHA256 signature verification for webhook endpoints
- **Token-based Auth**: Secret token validation for webhook requests
- **HTTPS Support**: TLS encryption for webhook delivery
- **Request Validation**: Comprehensive payload validation

### Data Protection
- **Local Storage**: All data stored locally on your system
- **Encrypted Databases**: SQLite databases with proper access controls
- **No Cloud Dependencies**: No external services required for core functionality

## 📈 Monitoring and Logging

### Webhook Monitoring
- **Delivery Status**: Real-time webhook delivery status tracking
- **Retry Monitoring**: Exponential backoff retry attempts with full logging
- **Error Tracking**: Comprehensive error logging and status codes

## 🐳 Docker Configuration

### Container Resources
```yaml
# Default resource limits
whatsapp-bridge:
  memory: 1G
  cpus: '0.5'

whatsapp-mcp:
  memory: 1G
  cpus: '0.5'

webhook-ui:
  memory: 500M
  cpus: '0.5'
```

### Environment Variables
```bash
# Bridge Service
TZ=UTC
STORE_DIR=store                  # where the databases and media live
API_PORT=8080
PUBLIC_BASE_URL=http://whatsapp-mcp-whatsapp-bridge-1:8080   # used in download_url / webhook payloads
DOWNLOAD_PACE_MS=250             # delay between items in a batch job
MEDIA_RETRY_TIMEOUT_S=30

# Auto-download (operational only — *which* chats are captured is set per listener in the UI)
AUTO_DOWNLOAD=on                 # kill switch for the whole pool; leaves every rule untouched
AUTO_DOWNLOAD_WORKERS=2
AUTO_DOWNLOAD_QUEUE_SIZE=100
AUTO_DOWNLOAD_TIMEOUT_S=60

# Retention (how long is set per listener in the UI; these are operational only)
RETENTION=on                     # off keeps every file, whatever the listeners say
RETENTION_DRY_RUN=true           # report only; only an explicit false deletes, a typo keeps dry run
RETENTION_SWEEP_INTERVAL_M=60
RETENTION_MAX_PER_SWEEP=500      # cap per sweep, oldest expiry first

# MCP Service
BRIDGE_HOST=whatsapp-bridge
DEBUG=true
TZ=UTC
```

The bridge also sets `stop_grace_period: 30s` so shutdown can finish a download already in
progress before the WhatsApp connection closes.

### Networking
- **Internal Network**: `whatsapp_internal` for service communication
- **External Network**: `n8n_n8n_traefik_network` for external access
- **Port Mapping**: Configurable port mappings for all services

## 🔧 Development

### Building from Source
```bash
# Build all containers
docker-compose build

# Build specific service
docker-compose build whatsapp-bridge

# Development mode with hot reload
docker-compose -f docker-compose.dev.yml up
```

### Testing
```bash
# Run bridge tests (63 tests)
cd whatsapp-bridge
go test ./...
go test ./... -race        # the auto-download pool is concurrent
go vet ./...

# Run the webhook UI tests (jsdom, in a node:22 container — no Node needed on the host)
./scripts/test-ui.sh

# After every deploy: check the RUNNING system, not the build output
./scripts/smoke.sh               # set EXPECT_SCHEMA when a migration ships

# Run MCP server tests
cd whatsapp-mcp-server
uv run pytest

# Test webhook delivery
cd whatsapp-bridge
./test-webhook-delivery.sh
```

### Database Management
```bash
# Access message database
sqlite3 store/messages.db

# Access WhatsApp store
sqlite3 store/whatsapp.db

# Backup databases
docker-compose exec whatsapp-bridge sqlite3 /app/store/messages.db .backup backup.db
```

## 🚨 Troubleshooting

### Common Issues

#### Authentication Problems
- **QR Code Issues**: Ensure terminal supports QR code display
- **Session Expired**: Delete session files and re-authenticate
- **Device Limit**: Remove old devices from WhatsApp settings

#### Container Issues
- **Port Conflicts**: Check for conflicting services on ports 8080-8089
- **Memory Issues**: Increase container memory limits in docker-compose.yml
- **Storage Issues**: Ensure sufficient disk space for databases
- **Your change didn't take effect**: always redeploy with
  `docker compose up -d --build --force-recreate <service>`. Plain `--build` **starts an
  existing container rather than replacing it**, so a freshly built image is silently
  ignored and the old binary keeps running. This has bitten twice in this project. The UI is
  a separate image (`Dockerfile.ui` bakes the static files in), so a change to
  `whatsapp-webhook-ui/` needs `webhook-ui` rebuilt too — rebuilding only the bridge leaves
  the browser on the old page. Verify by what the running system reports (the bridge logs
  `messages.db schema version N` at startup), never by "Built" or "Up/healthy".

#### Auto-Download Problems
- **A rule captures nothing**: check the message is actually audio — images, video and
  documents are never captured, whatever the trigger matches. Check the listener's logs for
  `auto_download` rows.
- **A rule with a phone number matches nothing**: this was fixed by LID expansion; confirm
  the bridge logged `Trigger N (sender=…) also matches [...]` at startup. `contains` and
  `regex` triggers are never expanded.
- **Nothing in the Logs view**: a download-only listener writes capture rows, not delivery
  rows. If the list is empty, nothing has matched yet.
- **Disable everything without touching rules**: set `AUTO_DOWNLOAD=off` on the bridge.

#### Webhook Problems
- **Delivery Failures**: Check webhook endpoint accessibility and SSL certificates
- **Authentication Errors**: Verify HMAC signature implementation

### Debug Commands
```bash
# Check container logs
docker-compose logs -f whatsapp-bridge
docker-compose logs -f whatsapp-mcp

# Check container health
docker-compose ps

# Access container shell
docker-compose exec whatsapp-bridge /bin/bash
```

### Performance Optimization
- **Database Optimization**: Regular VACUUM and index maintenance
- **Memory Management**: Monitor container memory usage
- **Network Optimization**: Use internal networking for service communication
- **Storage Optimization**: Regular cleanup of old media files

## 📚 Additional Resources

- **WhatsApp API Documentation**: [whatsmeow library](https://github.com/tulir/whatsmeow)
- **MCP Protocol**: [Model Context Protocol](https://modelcontextprotocol.io/)
- **Docker Documentation**: [Docker Compose](https://docs.docker.com/compose/)
- **Claude Desktop**: [Claude Desktop Integration](https://claude.ai/desktop)

## 🤝 Contributing

Contributions are welcome! Please read our contributing guidelines and submit pull requests for any improvements.

## 📄 License

This project is licensed under the MIT License - see the LICENSE file for details.

## 📞 Support

For support and questions:
- GitHub Issues: Create an issue for bug reports or feature requests
- Documentation: Check the comprehensive documentation in the project

---

