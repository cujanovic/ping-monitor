#!/bin/bash

# Ping Monitor Service Installer
# This script installs the ping monitor as a systemd service

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
SERVICE_NAME="ping-monitor"
SERVICE_USER="pingmon"
INSTALL_DIR="/opt/ping-monitor"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"

# Check if this is an update or fresh install
UPDATE_MODE=false
if [ -d "$INSTALL_DIR" ] && [ -f "$SERVICE_FILE" ]; then
    UPDATE_MODE=true
    echo -e "${YELLOW}🔄 Existing installation detected - Running in UPDATE mode${NC}"
else
    echo -e "${GREEN}🚀 Installing Ping Monitor Service${NC}"
fi
echo "================================================"

# Check if running as root
if [[ $EUID -ne 0 ]]; then
   echo -e "${RED}❌ This script must be run as root${NC}"
   echo "Please run: sudo $0"
   exit 1
fi

# Check if Go is installed
if ! command -v go &> /dev/null; then
    echo -e "${YELLOW}⚠️  Go is not installed. Installing Go...${NC}"
    
    # Install Go
    dnf update -y
    dnf install -y golang
    
    # Set GOPATH and add to PATH
    echo 'export GOPATH=$HOME/go' >> /etc/profile
    echo 'export PATH=$PATH:/usr/local/go/bin:$GOPATH/bin' >> /etc/profile
    source /etc/profile
fi

# Create service user (skip if updating)
if [ "$UPDATE_MODE" = false ]; then
    echo -e "${GREEN}👤 Creating service user...${NC}"
    if ! id "$SERVICE_USER" &>/dev/null; then
        useradd -r -s /bin/false -d "$INSTALL_DIR" "$SERVICE_USER"
        echo -e "${GREEN}✅ Service user created: $SERVICE_USER${NC}"
    else
        echo -e "${YELLOW}⚠️  Service user already exists: $SERVICE_USER${NC}"
    fi
else
    echo -e "${GREEN}👤 Service user already exists: $SERVICE_USER${NC}"
fi

# Stop service if updating
if [ "$UPDATE_MODE" = true ]; then
    echo -e "${YELLOW}⏹️  Stopping service for update...${NC}"
    systemctl stop "$SERVICE_NAME" 2>/dev/null || true
    echo -e "${GREEN}✅ Service stopped${NC}"
fi

# Save existing config to temporary location if updating
TEMP_CONFIG=""
if [ "$UPDATE_MODE" = true ] && [ -f "$INSTALL_DIR/config.json" ]; then
    echo -e "${YELLOW}💾 Preserving existing configuration...${NC}"
    TEMP_CONFIG=$(mktemp)
    cp "$INSTALL_DIR/config.json" "$TEMP_CONFIG"
    echo -e "${GREEN}✅ Configuration preserved${NC}"
fi

# Create installation directory
echo -e "${GREEN}📁 Creating installation directory...${NC}"
mkdir -p "$INSTALL_DIR"
chown "$SERVICE_USER:$SERVICE_USER" "$INSTALL_DIR"

# Copy files to installation directory
echo -e "${GREEN}📋 Copying service files...${NC}"
cp *.go "$INSTALL_DIR/"
cp go.mod "$INSTALL_DIR/"
cp go.sum "$INSTALL_DIR/" 2>/dev/null || true
cp config.json "$INSTALL_DIR/"

# Copy templates directory (required for HTTP interface)
echo -e "${GREEN}📁 Copying HTML templates and static files...${NC}"
mkdir -p "$INSTALL_DIR/templates"
cp templates/*.html "$INSTALL_DIR/templates/" 2>/dev/null || true
cp templates/*.js "$INSTALL_DIR/templates/" 2>/dev/null || true
cp templates/*.svg "$INSTALL_DIR/templates/" 2>/dev/null || true

# Restore existing config if this was an update
if [ "$UPDATE_MODE" = true ] && [ -n "$TEMP_CONFIG" ] && [ -f "$TEMP_CONFIG" ]; then
    cp "$TEMP_CONFIG" "$INSTALL_DIR/config.json"
    rm -f "$TEMP_CONFIG"
    echo -e "${GREEN}✅ Existing configuration restored${NC}"
fi

# Create one-time backup of original config for fresh installs only
if [ "$UPDATE_MODE" = false ] && [ ! -f "$INSTALL_DIR/config.json.original" ]; then
    cp "$INSTALL_DIR/config.json" "$INSTALL_DIR/config.json.original"
    echo -e "${GREEN}✅ Original configuration backed up to config.json.original${NC}"
fi

# Set proper ownership (includes all subdirectories like templates/)
chown -R "$SERVICE_USER:$SERVICE_USER" "$INSTALL_DIR"

# Build the service binary
echo -e "${GREEN}🔨 Building service binary...${NC}"
cd "$INSTALL_DIR"

# Install dependencies
echo "Installing Go dependencies..."
su -s /bin/bash -c "cd $INSTALL_DIR && go mod tidy" "$SERVICE_USER"

# Build the binary
echo "Building ping-monitor binary..."
su -s /bin/bash -c "cd $INSTALL_DIR && go build -o ping-monitor" "$SERVICE_USER"

# Make binary executable
chmod +x ping-monitor

echo -e "${GREEN}✅ Service binary built successfully${NC}"

# Create systemd service file
echo -e "${GREEN}⚙️  Creating systemd service...${NC}"
cat > "$SERVICE_FILE" << EOF
[Unit]
Description=Ping Monitor Service
Documentation=https://github.com/cujanovic/ping-monitor
After=network.target network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
Group=$SERVICE_USER
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/ping-monitor
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal
SyslogIdentifier=ping-monitor

# Security settings
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=$INSTALL_DIR

# Resource limits
LimitNOFILE=65536
MemoryMax=256M

[Install]
WantedBy=multi-user.target
EOF

# Reload systemd and enable service
echo -e "${GREEN}🔄 Reloading systemd and enabling service...${NC}"
systemctl daemon-reload
systemctl enable "$SERVICE_NAME"

echo -e "${GREEN}✅ Service installed and enabled${NC}"

# Create log directory
mkdir -p /var/log/ping-monitor
chown "$SERVICE_USER:$SERVICE_USER" /var/log/ping-monitor

echo ""
if [ "$UPDATE_MODE" = true ]; then
    echo -e "${GREEN}🎉 Update completed successfully!${NC}"
    echo "================================================"
    echo -e "${GREEN}What was updated:${NC}"
    echo "  • Service binary rebuilt with latest code"
    echo "  • Dependencies updated (go.mod, go.sum)"
    echo "  • Configuration preserved (your settings kept)"
    echo ""
    echo -e "${YELLOW}🔄 Starting service...${NC}"
    systemctl start "$SERVICE_NAME"
    sleep 2
    if systemctl is-active --quiet "$SERVICE_NAME"; then
        echo -e "${GREEN}✅ Service started successfully!${NC}"
    else
        echo -e "${RED}❌ Service failed to start. Check logs:${NC}"
        echo "  sudo journalctl -u $SERVICE_NAME -n 50"
    fi
else
    echo -e "${GREEN}🎉 Installation completed successfully!${NC}"
    echo "================================================"
    echo -e "${GREEN}Service Details:${NC}"
    echo "  • Service Name: $SERVICE_NAME"
    echo "  • Install Directory: $INSTALL_DIR"
    echo "  • Service User: $SERVICE_USER"
    echo "  • Config File: $INSTALL_DIR/config.json"
    echo ""
    echo -e "${YELLOW}⚠️  Next Steps:${NC}"
    echo "  1. Update your API key in: $INSTALL_DIR/config.json"
    echo "  2. Configure your targets in: $INSTALL_DIR/config.json"
    echo "  3. Start the service with: sudo systemctl start $SERVICE_NAME"
fi

echo ""
echo -e "${GREEN}Management Commands:${NC}"
echo "  • Start service:    sudo systemctl start $SERVICE_NAME"
echo "  • Stop service:     sudo systemctl stop $SERVICE_NAME"
echo "  • Restart service:  sudo systemctl restart $SERVICE_NAME"
echo "  • Check status:     sudo systemctl status $SERVICE_NAME"
echo "  • View logs:        sudo journalctl -u $SERVICE_NAME -f"
echo ""
echo -e "${GREEN}📝 Configuration:${NC}"
echo "  • Edit config:      sudo nano $INSTALL_DIR/config.json"
echo "  • Original backup:  $INSTALL_DIR/config.json.original"
echo ""
if [ "$UPDATE_MODE" = true ]; then
    echo -e "${GREEN}🔄 To update again in the future:${NC}"
else
    echo -e "${GREEN}🔄 To update in the future:${NC}"
fi
echo "  • Pull latest code from git"
echo "  • Run: sudo ./install.sh"
echo ""
echo -e "${GREEN}🚀 Service is ready!${NC}"
