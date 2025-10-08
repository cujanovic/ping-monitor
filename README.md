# Ping Monitor Service

A Go-based service that monitors IP addresses and sends email notifications when they become unreachable.

## Features

- **Ping Monitoring**: Continuously pings configured IP addresses and domains at specified intervals
- **Packet Loss Detection**: Alerts when packet loss exceeds configurable thresholds (not just complete failure)
- **Latency Monitoring**: Tracks response times and alerts on high latency
- **Email Notifications**: Sends alerts when targets go down, recover, or experience issues
- **Summary Reports**: Daily or weekly email digests with uptime statistics and performance metrics
- **Config Validation**: Comprehensive validation of configuration on startup
- **Alert Cooldown**: Prevents alert spam with configurable cooldown periods
- **Rate Limiting**: Protects email quota with configurable email rate limits
- **Concurrent Optimization**: Efficient handling of large numbers of targets with worker pool
- **Graceful Degradation**: Continues monitoring other targets even if some fail
- **Configurable Timeouts**: Per-target or global timeout settings
- **Flexible Configuration**: Per-target or global thresholds for latency and packet loss
- **Graceful Shutdown**: Handles SIGTERM and SIGINT signals properly
- **Comprehensive Logging**: Detailed logging with statistics and error recovery

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
  "packet_loss_threshold_percent": 50,
  "alert_cooldown_minutes": 15,
  "email_rate_limit_per_hour": 60,
  "max_concurrent_pings": 10,
  "default_timeout_seconds": 10,
  "summary_report_enabled": true,
  "summary_report_schedule": "daily",
  "summary_report_time": "09:00",
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
      "target": "192.168.1.1",
      "ping_time_threshold_ms": 50,
      "packet_loss_threshold_percent": 20,
      "timeout_seconds": 5
    },
    {
      "name": "Example Domain",
      "target": "example.com",
      "ping_time_threshold_ms": 500,
      "packet_loss_threshold_percent": 40,
      "timeout_seconds": 15
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

**Automated Installation (systemd-based Linux):**
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

**Manual Installation:**
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

#### Global Settings
- **ping_interval_seconds**: How often to ping targets (in seconds)
- **ping_count**: Number of ping packets to send per check (default: 3)
- **ping_time_threshold_ms**: Global latency threshold in milliseconds (default: 200ms)
- **packet_loss_threshold_percent**: Global packet loss threshold as percentage (default: 50%)
- **alert_cooldown_minutes**: Minimum time between repeat alerts for the same issue (default: 15 minutes)
- **email_rate_limit_per_hour**: Maximum emails to send per hour (default: 60, protects against quota exhaustion)
- **max_concurrent_pings**: Maximum number of concurrent ping operations (default: 10, optimizes for large target lists)
- **default_timeout_seconds**: Default timeout for ping operations in seconds (default: 10)
- **summary_report_enabled**: Enable daily/weekly summary reports (default: false)
- **summary_report_schedule**: Report frequency - "daily" or "weekly" (default: "daily")
- **summary_report_time**: Time to send reports in HH:MM format (default: "09:00")

#### Email Configuration
- **email**: Brevo email configuration
  - **api_key**: Your Brevo API key
  - **from**: Sender email address (must be verified in Brevo)
  - **to**: Recipient email address

#### Target Configuration
- **targets**: Array of targets to monitor
  - **name**: Human-readable name for the target
  - **target**: IP address or domain name to ping
  - **ping_time_threshold_ms** (optional): Per-target latency threshold in milliseconds. If not specified, uses global threshold
  - **packet_loss_threshold_percent** (optional): Per-target packet loss threshold as percentage. If not specified, uses global threshold
  - **timeout_seconds** (optional): Per-target timeout in seconds. If not specified, uses default_timeout_seconds

## Email Notifications

The service sends six types of email notifications plus optional summary reports:

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

### 🟠 Packet Loss Alert
```
Subject: 🟠 Ping Monitor Alert: Google DNS has PACKET LOSS

Ping Monitor Alert

Target: Google DNS
IP: 8.8.8.8
Status: PACKET LOSS
Time: 2024-01-15 14:30:25
Packet Loss: 60%
Threshold: 50%

This target is experiencing significant packet loss.
```

### 🟢 Packet Loss Recovery
```
Subject: 🟢 Ping Monitor Recovery: Google DNS packet loss NORMAL

Ping Monitor Recovery

Target: Google DNS
IP: 8.8.8.8
Status: PACKET LOSS NORMAL
Time: 2024-01-15 14:32:15
Packet Loss: 10%

This target's packet loss has returned to normal levels.
```

### 📊 Summary Report (Daily/Weekly)
```
Subject: 📊 Ping Monitor Daily Summary Report

Ping Monitor Daily Summary Report
Period: 23 hours 59 minutes
Report Generated: 2024-01-16 09:00:00

============================================================

Target: Google DNS (8.8.8.8)
  Uptime: 100.00% (288/288 checks successful)
  Failed Checks: 0
  Latency: avg=28.45ms, min=15.20ms, max=45.80ms
  High Latency Events: 0
  Avg Packet Loss: 0.0%
  Packet Loss Events: 0

Target: Local Router (192.168.1.1)
  Uptime: 95.83% (276/288 checks successful)
  Failed Checks: 12
  Latency: avg=2.15ms, min=1.20ms, max=5.50ms
  High Latency Events: 2
  Avg Packet Loss: 15.5%
  Packet Loss Events: 8

============================================================

Next daily report: 2024-01-17 09:00:00
```

## Logging

The service provides detailed logging:

```
2024/01/15 14:30:25 🎯 Ping Monitor Service Starting...
2024/01/15 14:30:25 🚀 Starting Ping Monitor with the following settings:
2024/01/15 14:30:25    • Targets: 7
2024/01/15 14:30:25    • Ping Interval: 30 seconds
2024/01/15 14:30:25    • Ping Count: 3
2024/01/15 14:30:25    • Packet Loss Threshold: 50%
2024/01/15 14:30:25    • Alert Cooldown: 15 minutes
2024/01/15 14:30:25    • Email Rate Limit: 60/hour
2024/01/15 14:30:25    • Max Concurrent Pings: 10
2024/01/15 14:30:25 🔀 Targets shuffled for randomized monitoring order
2024/01/15 14:30:25 📊 Distributing pings with 4.285s delay between targets for continuous monitoring
2024/01/15 14:30:25 ✅ All monitoring goroutines started
2024/01/15 14:30:25 ✓ Google DNS (IP: 8.8.8.8) - 3/3 packets received (0% loss), avg 28.45ms
2024/01/15 14:30:28 ✓ Cloudflare DNS (IP: 1.1.1.1) - 3/3 packets received (0% loss), avg 15.23ms
2024/01/15 14:30:30 ✓ Example Website (Domain: example.com) - 3/3 packets received (0% loss), avg 45.12ms
2024/01/15 14:30:35 ✓ Local Router (IP: 192.168.1.1) - 2/3 packets received (33% loss), avg 2.15ms
2024/01/15 14:31:05 ✓ Local Router (IP: 192.168.1.1) - 1/3 packets received (67% loss), avg 1.98ms
2024/01/15 14:31:05 🟠 ALERT: Local Router (IP: 192.168.1.1) has PACKET LOSS (67% >= 20%)
2024/01/15 14:31:05 📧 Email notification sent for Local Router (IP: 192.168.1.1) (packet_loss)
2024/01/15 14:32:15 ✓ Web Server (IP: 10.0.0.5) - 3/3 packets received (0% loss), avg 450.23ms
2024/01/15 14:32:15 🟡 ALERT: Web Server (IP: 10.0.0.5) has HIGH LATENCY (450.23ms > 200ms)
2024/01/15 14:32:15 📧 Email notification sent for Web Server (IP: 10.0.0.5) (slow)
```

## Advanced Features

### Summary Reports
Automatically generates and emails daily or weekly summary reports with:
- Uptime percentage for each target
- Success/failure statistics
- Latency metrics (average, min, max)
- Packet loss statistics
- Count of high latency and packet loss events

Configure with `summary_report_enabled`, `summary_report_schedule` (daily/weekly), and `summary_report_time` (HH:MM format).

### Config Validation
Comprehensive validation on startup checks:
- Required fields are present
- Values are within valid ranges
- Email addresses are properly formatted
- No duplicate target names or addresses
- Thresholds are reasonable
- Prevents common configuration mistakes

### Graceful Degradation
The system continues monitoring all targets even when issues occur:
- If one target fails, others continue unaffected
- Network errors don't stop the service
- Email failures are logged but monitoring continues
- Panic recovery ensures goroutines restart
- Errors are logged with context for troubleshooting

### Configurable Timeouts
Set different timeouts for different targets:
- Fast local networks: 5 seconds
- Regional servers: 10 seconds (default)
- International connections: 15-30 seconds
- Per-target overrides or global default

### Alert Cooldown
Prevents alert spam when targets flap up/down repeatedly. After sending an alert for a specific issue on a target, subsequent alerts for the same issue are suppressed for the cooldown period (default: 15 minutes).

### Email Rate Limiting
Protects your Brevo email quota (300 emails/day on free tier) by limiting emails sent per hour. Uses a sliding window to track recent emails. Alerts are logged even if rate limit is reached.

### Packet Loss Detection
Monitors not just complete failures but also partial packet loss. Perfect for detecting intermittent network issues before they become critical. Configure different thresholds per target.

### Concurrent Optimization
Uses a worker pool pattern to efficiently handle large numbers of targets. The `max_concurrent_pings` setting prevents overwhelming your network or system resources.

## Troubleshooting

### Common Issues

1. **Configuration validation errors on startup**:
   - Read the error message carefully - it lists all validation issues
   - Fix each issue in config.json
   - Common issues: invalid email format, out-of-range values, duplicate names

2. **Permission denied for ping**: The service uses unprivileged ping by default. If you need privileged ping, modify the `SetPrivileged(true)` in the code.

3. **Email not sending**: 
   - Verify Brevo API key is correct
   - Check that sender email is verified in Brevo
   - Ensure API key has proper permissions
   - Check if rate limit has been reached (view logs)

4. **Targets not responding**:
   - Verify IP addresses or domain names are correct
   - Check network connectivity and DNS resolution
   - Ensure targets allow ICMP packets
   - Try increasing timeout_seconds for slow connections
   
5. **Too many alerts (spam)**:
   - Increase `alert_cooldown_minutes` (default: 15)
   - Adjust thresholds to be less sensitive
   - Enable summary reports to reduce individual alerts

6. **Hitting email quota**:
   - Reduce `email_rate_limit_per_hour` (default: 60)
   - Increase `alert_cooldown_minutes` to reduce frequency
   - Use summary reports instead of individual alerts
   - Consider upgrading your Brevo plan

7. **Timeouts on specific targets**:
   - Increase `timeout_seconds` for that target
   - Check network path to target
   - Consider if target is appropriate for monitoring

8. **Summary reports not sending**:
   - Verify `summary_report_enabled` is true
   - Check `summary_report_time` format (HH:MM)
   - Ensure `summary_report_schedule` is "daily" or "weekly"
   - Reports send at the configured time

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

The project includes automated installation scripts for systemd-based Linux distributions:

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
