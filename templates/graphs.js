// Chart.js colors palette
const colors = [
	'#667eea', '#48bb78', '#ed8936', '#e53e3e', '#38b2ac',
	'#9f7aea', '#f56565', '#4fd1c5', '#fc8181', '#68d391',
	'#f6ad55', '#63b3ed', '#b794f4', '#fbb6ce', '#81e6d9'
];

let latencyChart = null;
let packetLossChart = null;
let cachedData = null;

// Check if target matches current filter (search + dropdown)
function targetMatchesFilter(target) {
	const searchTerm = document.getElementById('searchInput').value.toLowerCase().trim();
	const selectedTarget = document.getElementById('targetSelect').value;
	
	// If specific target selected in dropdown, only show that one
	if (selectedTarget !== 'all') {
		return target.address === selectedTarget;
	}
	
	// If search term exists, filter by it
	if (searchTerm) {
		return target.name.toLowerCase().includes(searchTerm) || 
		       target.address.toLowerCase().includes(searchTerm);
	}
	
	// No filter - show all
	return true;
}

// Update match info display
function updateMatchInfo(matchCount, totalCount) {
	const matchInfo = document.getElementById('matchInfo');
	const searchTerm = document.getElementById('searchInput').value.trim();
	
	if (searchTerm) {
		matchInfo.style.display = 'block';
		matchInfo.textContent = 'Showing ' + matchCount + ' of ' + totalCount + ' targets matching "' + searchTerm + '"';
	} else {
		matchInfo.style.display = 'none';
	}
}

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
	let allPoints = [];
	
	data.targets.forEach(function(target) {
		if (targetMatchesFilter(target)) {
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
	const ctx1 = document.getElementById('latencyChart').getContext('2d');
	const ctx2 = document.getElementById('packetLossChart').getContext('2d');
	
	// Destroy existing charts
	if (latencyChart) latencyChart.destroy();
	if (packetLossChart) packetLossChart.destroy();
	
	// Check if there's any data and count matches
	let totalPoints = 0;
	let matchCount = 0;
	data.targets.forEach(function(target) {
		if (targetMatchesFilter(target)) {
			totalPoints += target.points.length;
			matchCount++;
		}
	});
	
	// Update match info
	updateMatchInfo(matchCount, data.targets.length);
	
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
		if (!targetMatchesFilter(target)) {
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
				display: matchCount > 1,
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
		// Clear search when selecting specific target
		if (this.value !== 'all') {
			document.getElementById('searchInput').value = '';
		}
		if (cachedData) {
			updateCharts(cachedData);
		}
	});
	document.getElementById('refreshBtn').addEventListener('click', refreshData);
	
	// Search input - debounced
	var searchTimeout = null;
	document.getElementById('searchInput').addEventListener('input', function() {
		// Reset dropdown to "All" when searching
		document.getElementById('targetSelect').value = 'all';
		
		// Debounce search
		clearTimeout(searchTimeout);
		searchTimeout = setTimeout(function() {
			if (cachedData) {
				updateCharts(cachedData);
			}
		}, 300);
	});
	
	// Clear button
	document.getElementById('clearBtn').addEventListener('click', function() {
		document.getElementById('searchInput').value = '';
		document.getElementById('targetSelect').value = 'all';
		if (cachedData) {
			updateCharts(cachedData);
		}
	});
	
	// Initial load
	refreshData();
	
	// Auto-refresh every 60 seconds
	setInterval(refreshData, 60000);
});

