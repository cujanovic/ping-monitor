# Golang Ping Monitor Service

A Go-based service that monitors IP addresses and sends email notifications when they become unreachable.

## Features

- **Ping Monitoring**: Continuously pings configured IP addresses at specified intervals
- **Email Notifications**: Sends alerts when targets go down or come back up
- **Configurable**: JSON-based configuration for targets, intervals, and email settings
- **Graceful Shutdown**: Handles SIGTERM and SIGINT signals properly
- **Logging**: Comprehensive logging of ping results and notifications

## Email Service: Brevo (Recommended)

This service uses [Brevo](https://www.brevo.com/) (formerly Sendinblue) for reliable email delivery:

### **Why Brevo?**
- ✅ **Free Tier**: 300 emails/day, 9,000 emails/month
- ✅ **High Deliverability**: Excellent reputation for inbox delivery
- ✅ **Easy Setup**: Simple API key authentication
- ✅ **Reliable**: Enterprise-grade email infrastructure
- ✅ **No SMTP Complexity**: Direct API integration

## Setup Instructions

### 1. Install Dependencies

```bash
go mod tidy
```

### 2. Setup Brevo Account

1. **Create Brevo Account**:
   - Go to [brevo.com](https://www.brevo.com/)
   - Sign up for a free account
   - Verify your email address

2. **Get API Key**:
   - Login to your Brevo dashboard
   - Go to **Settings** → **API Keys**
   - Click **Create a new API key**
   - Give it a name (e.g., "Ping Monitor")
   - Copy the generated API key

3. **Configure Email Settings**:
   ```json
   {
     "email": {
       "api_key": "your-brevo-api-key-here",
       "from": "monitor@yourdomain.com",
       "to": "admin@yourcompany.com"
     }
   }
   ```

### 3. Domain Verification (Optional but Recommended)

For better deliverability, verify your sending domain:

1. In Brevo dashboard, go to **Senders & IP** → **Domains**
2. Add your domain (e.g., `yourdomain.com`)
3. Follow the DNS verification steps
4. Update the `from` email in config.json to use your verified domain

### 4. Configure Targets

Edit `config.json` to add your monitoring targets:

```json
{
  "ping_interval_seconds": 30,
  "ping_count": 3,
  "ping_time_threshold_ms": 200,
  "email": {
    "api_key": "your-brevo-api-key",
    "from": "monitor@yourdomain.com",
    "to": "admin@yourcompany.com"
  },
  "targets": [
    {
      "name": "Google DNS",
      "target": "8.8.8.8"
    },
    {
      "name": "Cloudflare DNS", 
      "target": "1.1.1.1"
    },
    {
      "name": "Local Router",
      "target": "192.168.1.1"
    },
    {
      "name": "Example Domain",
      "target": "example.com"
    }
  ]
}
```

### 5. Run the Service

#### Development:
```bash
go run main.go
```

#### Production:
```bash
go build -o ping-monitor
./ping-monitor
```

#### As a System Service:

**CentOS Stream 9 (Automated Installation):**
```bash
# Run the automated installer
sudo ./install.sh

# Start the service
sudo systemctl start ping-monitor

# Check status
sudo systemctl status ping-monitor

# View logs
sudo journalctl -u ping-monitor -f
```

**Manual Installation (Other Linux Distributions):**
```bash
# Create service file
sudo tee /etc/systemd/system/ping-monitor.service > /dev/null <<EOF
[Unit]
Description=Ping Monitor Service
After=network.target

[Service]
Type=simple
User=your-username
WorkingDirectory=/path/to/ping-monitor
ExecStart=/path/to/ping-monitor/ping-monitor
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

# Enable and start service
sudo systemctl daemon-reload
sudo systemctl enable ping-monitor
sudo systemctl start ping-monitor
```

## Configuration Options

### config.json Structure

- **ping_interval_seconds**: How often to ping targets (in seconds)
- **ping_count**: Number of ping packets to send per check (default: 3)
- **ping_time_threshold_ms**: Latency threshold in milliseconds (0 = disabled, recommended: 100-500ms)
- **email**: Brevo email configuration
  - **api_key**: Your Brevo API key
  - **from**: Sender email address (must be verified in Brevo)
  - **to**: Recipient email address
- **targets**: Array of targets to monitor
  - **name**: Human-readable name for the target
  - **target**: IP address or domain name to ping

## Email Notifications

The service sends four types of email notifications:

### 🔴 Down Alert
```
Subject: 🔴 Ping Monitor Alert: Google DNS is DOWN

Ping Monitor Alert

Target: Google DNS
IP: 8.8.8.8
Status: DOWN
Time: 2024-01-15 14:30:25

This target is not responding to ping requests.
```

### 🟢 Recovery Alert
```
Subject: 🟢 Ping Monitor Recovery: Google DNS is UP

Ping Monitor Recovery

Target: Google DNS
IP: 8.8.8.8
Status: UP
Time: 2024-01-15 14:35:10
Average RTT: 28.45 ms
Downtime: 5 minutes 7 seconds

This target is now responding to ping requests.
```

### 🟡 High Latency Alert
```
Subject: 🟡 Ping Monitor Alert: Google DNS has HIGH LATENCY

Ping Monitor Alert

Target: Google DNS
IP: 8.8.8.8
Status: HIGH LATENCY
Time: 2024-01-15 14:30:25
Average RTT: 450.23 ms
Threshold: 200 ms

This target is responding but with high latency.
```

### 🟢 Latency Recovery
```
Subject: 🟢 Ping Monitor Recovery: Google DNS latency NORMAL

Ping Monitor Recovery

Target: Google DNS
IP: 8.8.8.8
Status: LATENCY NORMAL
Time: 2024-01-15 14:32:15
Average RTT: 85.12 ms
Threshold: 200 ms

This target's latency has returned to normal.
```

## Logging

The service provides detailed logging:

```
2024/01/15 14:30:25 Starting ping monitor with 3 targets, checking every 30 seconds
2024/01/15 14:30:25 Distributing pings with 10s delay between targets for continuous monitoring
2024/01/15 14:30:25 ✓ Google DNS (IP: 8.8.8.8) - 3/3 packets received, avg 28.45ms
2024/01/15 14:30:28 ✓ Cloudflare DNS (IP: 1.1.1.1) - 3/3 packets received, avg 15.23ms
2024/01/15 14:30:30 ✓ Example Website (Domain: example.com) - 3/3 packets received, avg 45.12ms
2024/01/15 14:30:35 ✗ Local Router (IP: 192.168.1.1) - 0/3 packets received
2024/01/15 14:30:35 🔴 ALERT: Local Router (IP: 192.168.1.1) is now DOWN
2024/01/15 14:30:35 Email notification sent for Local Router (IP: 192.168.1.1)
2024/01/15 14:35:40 🟢 RECOVERY: Local Router (IP: 192.168.1.1) is now UP (was down for 5 minutes 7 seconds)
2024/01/15 14:35:40 Email notification sent for Local Router (IP: 192.168.1.1)
2024/01/15 14:31:15 ✓ Server-01 (IP: 10.0.0.5) - 3/3 packets received, avg 450.23ms
2024/01/15 14:31:15 🟡 ALERT: Server-01 (IP: 10.0.0.5) has HIGH LATENCY (450.23ms > 200ms)
2024/01/15 14:31:15 Email notification sent for Server-01 (IP: 10.0.0.5)
```

## Troubleshooting

### Common Issues

1. **Permission denied for ping**: The service uses unprivileged ping by default. If you need privileged ping, modify the `SetPrivileged(true)` in the code.

2. **Email not sending**: 
   - Verify Brevo API key is correct
   - Check that sender email is verified in Brevo
   - Ensure API key has proper permissions

3. **Targets not responding**:
   - Verify IP addresses or domain names are correct
   - Check network connectivity and DNS resolution
   - Ensure targets allow ICMP packets

### Testing

Test individual targets:
```bash
ping -c 3 8.8.8.8
```

Test Brevo configuration:
```bash
# Test your Brevo API key
curl -X POST "https://api.brevo.com/v3/smtp/email" \
  -H "accept: application/json" \
  -H "api-key: YOUR_API_KEY" \
  -H "content-type: application/json" \
  -d '{
    "sender": {"name": "Test", "email": "test@yourdomain.com"},
    "to": [{"email": "admin@yourdomain.com"}],
    "subject": "Test Email",
    "textContent": "This is a test email from Brevo"
  }'
```

## Security Considerations

- Store Brevo API key securely (use environment variables in production)
- Use a dedicated monitoring email account
- Implement proper firewall rules for the monitoring server
- Consider using environment variables for sensitive configuration:
  ```bash
  export BREVO_API_KEY="your-api-key"
  # Then update config.json to use: "api_key": "$BREVO_API_KEY"
  ```

## Installation Scripts

The project includes automated installation scripts for CentOS Stream 9:

#### **Install Script (`install.sh`)**
```bash
# Make executable and run
chmod +x install.sh
sudo ./install.sh
```

**What the install script does:**
- ✅ Installs Go if not present
- ✅ Creates dedicated service user (`pingmon`)
- ✅ Copies files to `/opt/ping-monitor`
- ✅ Builds the service binary
- ✅ Creates systemd service file
- ✅ Enables and starts the service
- ✅ Sets up proper security and resource limits

#### **Uninstall Script (`uninstall.sh`)**
```bash
# Make executable and run
chmod +x uninstall.sh
sudo ./uninstall.sh
```

**What the uninstall script does:**
- ✅ Stops and disables the service
- ✅ Removes systemd service file
- ✅ Removes installation directory
- ✅ Removes service user
- ✅ Cleans up log files

### **Service Management Commands**

After installation, use these commands to manage the service:

```bash
# Start the service
sudo systemctl start ping-monitor

# Stop the service
sudo systemctl stop ping-monitor

# Restart the service
sudo systemctl restart ping-monitor

# Check service status
sudo systemctl status ping-monitor

# View real-time logs
sudo journalctl -u ping-monitor -f

# View recent logs
sudo journalctl -u ping-monitor --since "1 hour ago"

# Enable auto-start on boot
sudo systemctl enable ping-monitor

# Disable auto-start on boot
sudo systemctl disable ping-monitor
```

### **Configuration After Installation**

After installation, update your configuration:

```bash
# Edit the configuration file
sudo nano /opt/ping-monitor/config.json

# Restart the service after changes
sudo systemctl restart ping-monitor
```

### **Updating the Service**

The `install.sh` script automatically detects existing installations and runs in **UPDATE mode**:

```bash
# Pull the latest code from git
cd /path/to/ping-monitor
git pull

# Run the installer (it will detect the existing installation)
sudo ./install.sh
```

**What happens during an update:**
- ✅ **Stops the service** temporarily
- ✅ **Preserves your config.json** (automatically restored after update)
- ✅ **Updates the binary** with latest code
- ✅ **Updates dependencies** (go.mod, go.sum)
- ✅ **Restarts the service** automatically
- ✅ **Verifies service started** successfully

**Configuration management:**
- Your existing `config.json` is **always preserved** during updates
- A one-time backup `config.json.original` is created on first install
- No multiple backup files created (keeps directory clean)

**To restore original configuration:**
```bash
# Stop the service
sudo systemctl stop ping-monitor

# Restore from original backup
sudo cp /opt/ping-monitor/config.json.original /opt/ping-monitor/config.json

# Restart the service
sudo systemctl start ping-monitor
```

## License

This project is open source and available under the MIT License.
