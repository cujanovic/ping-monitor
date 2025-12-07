// Chart.js colors palette
const colors = [
	'#667eea', '#48bb78', '#ed8936', '#e53e3e', '#38b2ac',
	'#9f7aea', '#f56565', '#4fd1c5', '#fc8181', '#68d391',
	'#f6ad55', '#63b3ed', '#b794f4', '#fbb6ce', '#81e6d9'
];

let latencyChart = null;
let packetLossChart = null;
let cachedData = null;

async function fetchData() {
	const hours = document.getElementById('hoursSelect').value;
	try {
		const response = await fetch('/api/latency-history?hours=' + hours);
		cachedData = await response.json();
		return cachedData;
	} catch (error) {
		console.error('Failed to fetch data:', error);
		return null;
	}
}

function updateStats(data) {
	const selectedTarget = document.getElementById('targetSelect').value;
	let allPoints = [];
	
	data.targets.forEach(function(target) {
		if (selectedTarget === 'all' || selectedTarget === target.address) {
			allPoints = allPoints.concat(target.points.filter(function(p) { return p.success; }));
		}
	});
	
	if (allPoints.length > 0) {
		const latencies = allPoints.map(function(p) { return p.latency_ms; });
		const sum = latencies.reduce(function(a, b) { return a + b; }, 0);
		document.getElementById('avgLatency').textContent = (sum / latencies.length).toFixed(2);
		document.getElementById('maxLatency').textContent = Math.max.apply(null, latencies).toFixed(2);
		document.getElementById('minLatency').textContent = Math.min.apply(null, latencies).toFixed(2);
		document.getElementById('dataPoints').textContent = allPoints.length.toLocaleString();
	} else {
		document.getElementById('avgLatency').textContent = '--';
		document.getElementById('maxLatency').textContent = '--';
		document.getElementById('minLatency').textContent = '--';
		document.getElementById('dataPoints').textContent = '0';
	}
}

function updateCharts(data) {
	const selectedTarget = document.getElementById('targetSelect').value;
	const ctx1 = document.getElementById('latencyChart').getContext('2d');
	const ctx2 = document.getElementById('packetLossChart').getContext('2d');
	
	// Destroy existing charts
	if (latencyChart) latencyChart.destroy();
	if (packetLossChart) packetLossChart.destroy();
	
	// Check if there's any data
	let totalPoints = 0;
	data.targets.forEach(function(target) {
		if (selectedTarget === 'all' || selectedTarget === target.address) {
			totalPoints += target.points.length;
		}
	});
	
	// Show/hide no data message
	const noDataMsg = document.getElementById('noDataMessage');
	const latencyContainer = document.getElementById('latencyChartContainer');
	const packetLossContainer = document.getElementById('packetLossChartContainer');
	
	if (totalPoints === 0) {
		noDataMsg.style.display = 'block';
		latencyContainer.style.display = 'none';
		packetLossContainer.style.display = 'none';
		updateStats(data);
		return;
	} else {
		noDataMsg.style.display = 'none';
		latencyContainer.style.display = 'block';
		packetLossContainer.style.display = 'block';
	}
	
	// Prepare datasets
	const latencyDatasets = [];
	const packetLossDatasets = [];
	
	data.targets.forEach(function(target, index) {
		if (selectedTarget !== 'all' && selectedTarget !== target.address) {
			return;
		}
		
		const color = colors[index % colors.length];
		
		// Latency dataset
		latencyDatasets.push({
			label: target.name,
			data: target.points.filter(function(p) { return p.success; }).map(function(p) {
				return { x: new Date(p.timestamp), y: p.latency_ms };
			}),
			borderColor: color,
			backgroundColor: color + '20',
			borderWidth: 2,
			pointRadius: 0,
			pointHoverRadius: 4,
			fill: false,
			tension: 0.3
		});
		
		// Packet loss dataset
		packetLossDatasets.push({
			label: target.name,
			data: target.points.map(function(p) {
				return { x: new Date(p.timestamp), y: p.packet_loss };
			}),
			borderColor: color,
			backgroundColor: color + '40',
			borderWidth: 2,
			pointRadius: 0,
			pointHoverRadius: 4,
			fill: true,
			tension: 0.3
		});
	});
	
	const chartOptions = {
		responsive: true,
		maintainAspectRatio: false,
		interaction: {
			intersect: false,
			mode: 'index'
		},
		plugins: {
			legend: {
				display: selectedTarget === 'all',
				position: 'bottom',
				labels: {
					color: '#b0b0b0',
					font: { family: "'SF Mono', Monaco, 'Courier New', monospace", size: 11 },
					boxWidth: 12,
					padding: 15
				}
			},
			tooltip: {
				backgroundColor: '#1e1e1e',
				titleColor: '#e0e0e0',
				bodyColor: '#b0b0b0',
				borderColor: '#404040',
				borderWidth: 1,
				padding: 12,
				callbacks: {
					title: function(items) {
						return new Date(items[0].parsed.x).toLocaleString();
					}
				}
			}
		},
		scales: {
			x: {
				type: 'time',
				time: {
					displayFormats: {
						hour: 'HH:mm',
						day: 'MMM d'
					}
				},
				grid: { color: '#404040' },
				ticks: { color: '#b0b0b0', font: { size: 10 } }
			},
			y: {
				beginAtZero: true,
				grid: { color: '#404040' },
				ticks: { color: '#b0b0b0', font: { size: 10 } }
			}
		}
	};
	
	// Create latency chart
	latencyChart = new Chart(ctx1, {
		type: 'line',
		data: { datasets: latencyDatasets },
		options: Object.assign({}, chartOptions, {
			scales: Object.assign({}, chartOptions.scales, {
				y: Object.assign({}, chartOptions.scales.y, {
					title: { display: true, text: 'Latency (ms)', color: '#b0b0b0' }
				})
			})
		})
	});
	
	// Create packet loss chart
	packetLossChart = new Chart(ctx2, {
		type: 'line',
		data: { datasets: packetLossDatasets },
		options: Object.assign({}, chartOptions, {
			scales: Object.assign({}, chartOptions.scales, {
				y: Object.assign({}, chartOptions.scales.y, {
					max: 100,
					title: { display: true, text: 'Packet Loss (%)', color: '#b0b0b0' }
				})
			})
		})
	});
	
	updateStats(data);
}

async function refreshData() {
	const data = await fetchData();
	if (data) {
		updateCharts(data);
	}
}

// Initialize when DOM is ready
document.addEventListener('DOMContentLoaded', function() {
	// Event listeners
	document.getElementById('hoursSelect').addEventListener('change', refreshData);
	document.getElementById('targetSelect').addEventListener('change', function() {
		if (cachedData) {
			updateCharts(cachedData);
		}
	});
	
	// Initial load
	refreshData();
	
	// Auto-refresh every 60 seconds
	setInterval(refreshData, 60000);
});

