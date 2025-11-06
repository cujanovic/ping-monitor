#!/bin/bash

# Ping Monitor Service Uninstaller
# This script removes the ping monitor systemd service

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

echo -e "${RED}🗑️  Uninstalling Ping Monitor Service${NC}"
echo "================================================"

# Check if running as root
if [[ $EUID -ne 0 ]]; then
   echo -e "${RED}❌ This script must be run as root${NC}"
   echo "Please run: sudo $0"
   exit 1
fi

# Stop and disable service
echo -e "${YELLOW}⏹️  Stopping service...${NC}"
if systemctl is-active --quiet "$SERVICE_NAME"; then
    systemctl stop "$SERVICE_NAME"
    echo -e "${GREEN}✅ Service stopped${NC}"
else
    echo -e "${YELLOW}⚠️  Service was not running${NC}"
fi

# Disable service
echo -e "${YELLOW}🔌 Disabling service...${NC}"
systemctl disable "$SERVICE_NAME" 2>/dev/null || true
echo -e "${GREEN}✅ Service disabled${NC}"

# Remove systemd service file
echo -e "${YELLOW}🗑️  Removing systemd service file...${NC}"
if [ -f "$SERVICE_FILE" ]; then
    rm -f "$SERVICE_FILE"
    echo -e "${GREEN}✅ Service file removed${NC}"
else
    echo -e "${YELLOW}⚠️  Service file not found${NC}"
fi

# Reload systemd
echo -e "${YELLOW}🔄 Reloading systemd...${NC}"
systemctl daemon-reload
echo -e "${GREEN}✅ Systemd reloaded${NC}"

# Remove installation directory
echo -e "${YELLOW}📁 Removing installation directory...${NC}"
if [ -d "$INSTALL_DIR" ]; then
    rm -rf "$INSTALL_DIR"
    echo -e "${GREEN}✅ Installation directory removed${NC}"
else
    echo -e "${YELLOW}⚠️  Installation directory not found${NC}"
fi

# Remove service user
echo -e "${YELLOW}👤 Removing service user...${NC}"
if id "$SERVICE_USER" &>/dev/null; then
    userdel "$SERVICE_USER" 2>/dev/null || true
    echo -e "${GREEN}✅ Service user removed${NC}"
else
    echo -e "${YELLOW}⚠️  Service user not found${NC}"
fi

# Remove log directory
echo -e "${YELLOW}📋 Removing log directory...${NC}"
if [ -d "/var/log/ping-monitor" ]; then
    rm -rf /var/log/ping-monitor
    echo -e "${GREEN}✅ Log directory removed${NC}"
else
    echo -e "${YELLOW}⚠️  Log directory not found${NC}"
fi

echo ""
echo -e "${GREEN}🎉 Uninstallation completed successfully!${NC}"
echo "================================================"
echo -e "${GREEN}Removed:${NC}"
echo "  • Service: $SERVICE_NAME"
echo "  • User: $SERVICE_USER"
echo "  • Directory: $INSTALL_DIR (including state.json)"
echo "  • Logs: /var/log/ping-monitor"
echo ""
echo -e "${YELLOW}⚠️  Note: Go installation was not removed${NC}"
echo "  If you want to remove Go, run: sudo dnf remove golang"
