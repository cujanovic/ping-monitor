// Chart.js colors palette
var colors = [
	'#667eea', '#48bb78', '#ed8936', '#e53e3e', '#38b2ac',
	'#9f7aea', '#f56565', '#4fd1c5', '#fc8181', '#68d391',
	'#f6ad55', '#63b3ed', '#b794f4', '#fbb6ce', '#81e6d9'
];

// Helper function to format target label with disabled indicator
function formatTargetLabel(target) {
	// Handle null/undefined safely
	if (!target || !target.name) {
		return 'Unknown';
	}
	if (target.is_disabled) {
		return target.name + ' (Disabled)';
	}
	return target.name;
}

// Chart instances
var charts = {
	latency: null,
	packetLoss: null,
	percentiles: null,
	successRate: null,
	distribution: null,
	jitter: null,
	hourlyPattern: null,
	comparison: null,
	heatmap: null
};

var cachedData = null;

// Check if target matches current filter (search + dropdown)
function targetMatchesFilter(target) {
	var searchInput = document.getElementById('searchInput');
	var targetSelect = document.getElementById('targetSelect');
	var searchTerm = searchInput ? searchInput.value.toLowerCase().trim() : '';
	var selectedTarget = targetSelect ? targetSelect.value : 'all';
	
	if (selectedTarget !== 'all') {
		return target.address === selectedTarget;
	}
	
	if (searchTerm) {
		return target.name.toLowerCase().indexOf(searchTerm) !== -1 || 
		       target.address.toLowerCase().indexOf(searchTerm) !== -1;
	}
	
	return true;
}

// Update match info display
function updateMatchInfo(matchCount, totalCount) {
	var matchInfo = document.getElementById('matchInfo');
	var searchInput = document.getElementById('searchInput');
	var searchTerm = searchInput ? searchInput.value.trim() : '';
	
	if (searchTerm && matchInfo) {
		matchInfo.style.display = 'block';
		matchInfo.textContent = 'Showing ' + matchCount + ' of ' + totalCount + ' targets matching "' + searchTerm + '"';
	} else if (matchInfo) {
		matchInfo.style.display = 'none';
	}
}

// Calculate percentile
function percentile(arr, p) {
	if (arr.length === 0) return 0;
	var sorted = arr.slice().sort(function(a, b) { return a - b; });
	var index = Math.ceil((p / 100) * sorted.length) - 1;
	return sorted[Math.max(0, index)];
}

// Calculate standard deviation (jitter)
function standardDeviation(arr) {
	if (arr.length === 0) return 0;
	var mean = arr.reduce(function(a, b) { return a + b; }, 0) / arr.length;
	var squaredDiffs = arr.map(function(x) { return Math.pow(x - mean, 2); });
	var avgSquaredDiff = squaredDiffs.reduce(function(a, b) { return a + b; }, 0) / arr.length;
	return Math.sqrt(avgSquaredDiff);
}

// Show/hide loading bar
function showLoading() {
	var loadingBar = document.getElementById('loadingBar');
	if (loadingBar) {
		loadingBar.classList.add('show');
	}
}

function hideLoading() {
	var loadingBar = document.getElementById('loadingBar');
	if (loadingBar) {
		loadingBar.classList.remove('show');
	}
}

// Fetch latency data
async function fetchData() {
	var hoursSelect = document.getElementById('hoursSelect');
	var hours = hoursSelect ? hoursSelect.value : 12;
	try {
		var response = await fetch('/api/latency-history?hours=' + hours);
		
		// Handle authentication errors - redirect to login
		if (response.status === 401) {
			window.location.href = '/login?return=' + encodeURIComponent('/reports/graphs');
			return null;
		}
		
		if (!response.ok) {
			throw new Error('HTTP ' + response.status);
		}
		
		cachedData = await response.json();
		return cachedData;
	} catch (error) {
		console.error('Failed to fetch data:', error);
		return null;
	}
}

// Update stats cards
function updateStats(data) {
	var allPoints = [];
	var totalPoints = 0;
	var successfulPoints = 0;
	
	data.targets.forEach(function(target) {
		if (targetMatchesFilter(target)) {
			target.points.forEach(function(p) {
				totalPoints++;
				if (p.success) {
					successfulPoints++;
					allPoints.push(p.latency_ms);
				}
			});
		}
	});
	
	if (allPoints.length > 0) {
		var sum = allPoints.reduce(function(a, b) { return a + b; }, 0);
		document.getElementById('avgLatency').textContent = (sum / allPoints.length).toFixed(1);
		document.getElementById('p50Latency').textContent = percentile(allPoints, 50).toFixed(1);
		document.getElementById('p95Latency').textContent = percentile(allPoints, 95).toFixed(1);
		document.getElementById('p99Latency').textContent = percentile(allPoints, 99).toFixed(1);
		document.getElementById('jitter').textContent = standardDeviation(allPoints).toFixed(1);
		document.getElementById('dataPoints').textContent = allPoints.length.toLocaleString();
	} else {
		document.getElementById('avgLatency').textContent = '--';
		document.getElementById('p50Latency').textContent = '--';
		document.getElementById('p95Latency').textContent = '--';
		document.getElementById('p99Latency').textContent = '--';
		document.getElementById('jitter').textContent = '--';
		document.getElementById('dataPoints').textContent = '0';
	}
	
	var successRate = totalPoints > 0 ? (successfulPoints / totalPoints * 100) : 0;
	document.getElementById('successRate').textContent = successRate.toFixed(1);
}

// Reset zoom for a specific chart
function resetZoom(chartName) {
	if (charts[chartName]) {
		charts[chartName].resetZoom();
	}
}

// Export data to CSV
function exportToCSV(data) {
	if (!data || !data.targets || data.targets.length === 0) {
		alert('No data available to export');
		return;
	}
	
	var csv = [];
	// CSV header
	csv.push('Target Name,Target Address,Timestamp,Latency (ms),Success,Packet Loss (%)');
	
	// Filter targets based on current filter
	var filteredTargets = data.targets.filter(function(target) {
		return targetMatchesFilter(target);
	});
	
	// Add data rows
	filteredTargets.forEach(function(target) {
		target.points.forEach(function(point) {
			var timestamp = new Date(point.timestamp).toISOString();
			var row = [
				escapeCSV(target.name),
				escapeCSV(target.address),
				timestamp,
				point.latency_ms.toFixed(2),
				point.success ? 'true' : 'false',
				point.packet_loss.toString()
			];
			csv.push(row.join(','));
		});
	});
	
	// Create download
	var csvContent = csv.join('\n');
	var blob = new Blob([csvContent], { type: 'text/csv;charset=utf-8;' });
	var url = URL.createObjectURL(blob);
	var link = document.createElement('a');
	link.href = url;
	link.download = 'ping-monitor-data-' + new Date().toISOString().split('T')[0] + '.csv';
	document.body.appendChild(link);
	link.click();
	document.body.removeChild(link);
	URL.revokeObjectURL(url);
}

// Export data to JSON
function exportToJSON(data) {
	if (!data || !data.targets || data.targets.length === 0) {
		alert('No data available to export');
		return;
	}
	
	// Filter targets based on current filter
	var filteredData = {
		exported_at: new Date().toISOString(),
		time_range_hours: document.getElementById('hoursSelect') ? document.getElementById('hoursSelect').value : 24,
		targets: data.targets.filter(function(target) {
			return targetMatchesFilter(target);
		}).map(function(target) {
			return {
				name: target.name,
				address: target.address,
				points: target.points.map(function(point) {
					return {
						timestamp: point.timestamp,
						latency_ms: point.latency_ms,
						success: point.success,
						packet_loss: point.packet_loss
					};
				})
			};
		})
	};
	
	// Create download
	var jsonContent = JSON.stringify(filteredData, null, 2);
	var blob = new Blob([jsonContent], { type: 'application/json;charset=utf-8;' });
	var url = URL.createObjectURL(blob);
	var link = document.createElement('a');
	link.href = url;
	link.download = 'ping-monitor-data-' + new Date().toISOString().split('T')[0] + '.json';
	document.body.appendChild(link);
	link.click();
	document.body.removeChild(link);
	URL.revokeObjectURL(url);
}

// Escape CSV field values
function escapeCSV(value) {
	if (value === null || value === undefined) {
		return '';
	}
	var str = String(value);
	// If contains comma, quote, or newline, wrap in quotes and escape quotes
	if (str.indexOf(',') !== -1 || str.indexOf('"') !== -1 || str.indexOf('\n') !== -1) {
		return '"' + str.replace(/"/g, '""') + '"';
	}
	return str;
}

// Zoom plugin options for time-based charts
function getZoomOptions() {
	return {
		zoom: {
			wheel: {
				enabled: true,
				modifierKey: null // no modifier key needed
			},
			pinch: {
				enabled: true
			},
			drag: {
				enabled: false // use pan instead
			},
			mode: 'x' // only zoom horizontally
		},
		pan: {
			enabled: true,
			mode: 'x',
			modifierKey: null
		},
		limits: {
			x: { minRange: 60 * 1000 } // minimum 1 minute range
		}
	};
}

// Common chart options
function getBaseChartOptions() {
	return {
		responsive: true,
		maintainAspectRatio: false,
		interaction: { intersect: false, mode: 'index' },
		plugins: {
			legend: {
				display: false,
				position: 'bottom',
				labels: {
					color: '#b0b0b0',
					font: { family: "'SF Mono', Monaco, monospace", size: 10 },
					boxWidth: 10,
					padding: 10
				}
			},
			tooltip: {
				backgroundColor: '#1e1e1e',
				titleColor: '#e0e0e0',
				bodyColor: '#b0b0b0',
				borderColor: '#404040',
				borderWidth: 1,
				padding: 10
			},
			zoom: getZoomOptions()
		},
		scales: {
			x: {
				type: 'time',
				time: { 
					displayFormats: { 
						millisecond: 'HH:mm:ss.SSS',
						second: 'HH:mm:ss',
						minute: 'HH:mm',
						hour: 'HH:mm', 
						day: 'MMM d',
						week: 'MMM d',
						month: 'MMM yyyy',
						quarter: 'MMM yyyy',
						year: 'yyyy'
					},
					tooltipFormat: 'MMM d, HH:mm:ss'
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
}

// Destroy chart safely
function destroyChart(name) {
	if (charts[name]) {
		charts[name].destroy();
		charts[name] = null;
	}
}

// Create latency chart
function createLatencyChart(data) {
	destroyChart('latency');
	var ctx = document.getElementById('latencyChart');
	if (!ctx) return;
	
	var datasets = [];
	var matchCount = 0;
	
	data.targets.forEach(function(target, index) {
		if (!targetMatchesFilter(target)) return;
		matchCount++;
		
		datasets.push({
			label: formatTargetLabel(target),
			data: target.points.filter(function(p) { return p.success; }).map(function(p) {
				return { x: new Date(p.timestamp), y: p.latency_ms };
			}),
			borderColor: colors[index % colors.length],
			backgroundColor: colors[index % colors.length] + '20',
			borderWidth: 2,
			pointRadius: 0,
			pointHoverRadius: 4,
			fill: false,
			tension: 0.3
		});
	});
	
	var options = getBaseChartOptions();
	options.plugins.legend.display = matchCount > 1;
	options.scales.y.title = { display: true, text: 'Latency (ms)', color: '#b0b0b0' };
	
	charts.latency = new Chart(ctx, { type: 'line', data: { datasets: datasets }, options: options });
}

// Create packet loss chart
function createPacketLossChart(data) {
	destroyChart('packetLoss');
	var ctx = document.getElementById('packetLossChart');
	if (!ctx) return;
	
	var datasets = [];
	var matchCount = 0;
	
	data.targets.forEach(function(target, index) {
		if (!targetMatchesFilter(target)) return;
		matchCount++;
		
		datasets.push({
			label: formatTargetLabel(target),
			data: target.points.map(function(p) {
				return { x: new Date(p.timestamp), y: p.packet_loss };
			}),
			borderColor: colors[index % colors.length],
			backgroundColor: colors[index % colors.length] + '40',
			borderWidth: 2,
			pointRadius: 0,
			fill: true,
			tension: 0.3
		});
	});
	
	var options = getBaseChartOptions();
	options.plugins.legend.display = matchCount > 1;
	options.scales.y.max = 100;
	options.scales.y.title = { display: true, text: 'Packet Loss (%)', color: '#b0b0b0' };
	
	charts.packetLoss = new Chart(ctx, { type: 'line', data: { datasets: datasets }, options: options });
}

// Create percentiles chart
function createPercentilesChart(data) {
	destroyChart('percentiles');
	var ctx = document.getElementById('percentilesChart');
	if (!ctx) return;
	
	// Group points by time window (5-minute buckets)
	var buckets = {};
	
	data.targets.forEach(function(target) {
		if (!targetMatchesFilter(target)) return;
		
		target.points.forEach(function(p) {
			if (!p.success) return;
			var ts = new Date(p.timestamp);
			var bucketKey = Math.floor(ts.getTime() / (5 * 60 * 1000)) * (5 * 60 * 1000);
			if (!buckets[bucketKey]) buckets[bucketKey] = [];
			buckets[bucketKey].push(p.latency_ms);
		});
	});
	
	var p50Data = [], p90Data = [], p95Data = [], p99Data = [];
	Object.keys(buckets).sort().forEach(function(key) {
		var ts = new Date(parseInt(key));
		var values = buckets[key];
		p50Data.push({ x: ts, y: percentile(values, 50) });
		p90Data.push({ x: ts, y: percentile(values, 90) });
		p95Data.push({ x: ts, y: percentile(values, 95) });
		p99Data.push({ x: ts, y: percentile(values, 99) });
	});
	
	var options = getBaseChartOptions();
	options.plugins.legend.display = true;
	options.scales.y.title = { display: true, text: 'Latency (ms)', color: '#b0b0b0' };
	
	charts.percentiles = new Chart(ctx, {
		type: 'line',
		data: {
			datasets: [
				{ label: 'P50', data: p50Data, borderColor: '#48bb78', borderWidth: 2, pointRadius: 0, tension: 0.3 },
				{ label: 'P90', data: p90Data, borderColor: '#ed8936', borderWidth: 2, pointRadius: 0, tension: 0.3 },
				{ label: 'P95', data: p95Data, borderColor: '#e53e3e', borderWidth: 2, pointRadius: 0, tension: 0.3 },
				{ label: 'P99', data: p99Data, borderColor: '#9f7aea', borderWidth: 2, pointRadius: 0, tension: 0.3 }
			]
		},
		options: options
	});
}

// Create success rate chart
function createSuccessRateChart(data) {
	destroyChart('successRate');
	var ctx = document.getElementById('successRateChart');
	if (!ctx) return;
	
	// Group by 5-minute buckets
	var buckets = {};
	
	data.targets.forEach(function(target) {
		if (!targetMatchesFilter(target)) return;
		
		target.points.forEach(function(p) {
			var ts = new Date(p.timestamp);
			var bucketKey = Math.floor(ts.getTime() / (5 * 60 * 1000)) * (5 * 60 * 1000);
			if (!buckets[bucketKey]) buckets[bucketKey] = { success: 0, total: 0 };
			buckets[bucketKey].total++;
			if (p.success) buckets[bucketKey].success++;
		});
	});
	
	var chartData = [];
	Object.keys(buckets).sort().forEach(function(key) {
		var ts = new Date(parseInt(key));
		var rate = (buckets[key].success / buckets[key].total) * 100;
		chartData.push({ x: ts, y: rate });
	});
	
	var options = getBaseChartOptions();
	options.scales.y.min = 0;
	options.scales.y.max = 100;
	options.scales.y.title = { display: true, text: 'Success Rate (%)', color: '#b0b0b0' };
	
	charts.successRate = new Chart(ctx, {
		type: 'line',
		data: {
			datasets: [{
				label: 'Success Rate',
				data: chartData,
				borderColor: '#48bb78',
				backgroundColor: '#48bb7840',
				borderWidth: 2,
				pointRadius: 0,
				fill: true,
				tension: 0.3
			}]
		},
		options: options
	});
}

// Create distribution chart (histogram)
function createDistributionChart(data) {
	destroyChart('distribution');
	var ctx = document.getElementById('distributionChart');
	if (!ctx) return;
	
	var allLatencies = [];
	data.targets.forEach(function(target) {
		if (!targetMatchesFilter(target)) return;
		target.points.forEach(function(p) {
			if (p.success) allLatencies.push(p.latency_ms);
		});
	});
	
	if (allLatencies.length === 0) return;
	
	// Create histogram buckets
	var max = Math.max.apply(null, allLatencies);
	var bucketSize = Math.max(10, Math.ceil(max / 15));
	var buckets = {};
	
	allLatencies.forEach(function(lat) {
		var bucket = Math.floor(lat / bucketSize) * bucketSize;
		buckets[bucket] = (buckets[bucket] || 0) + 1;
	});
	
	var labels = [];
	var values = [];
	var bgColors = [];
	
	Object.keys(buckets).map(Number).sort(function(a, b) { return a - b; }).forEach(function(bucket) {
		labels.push(bucket + '-' + (bucket + bucketSize) + 'ms');
		values.push(buckets[bucket]);
		// Color based on latency
		if (bucket < 50) bgColors.push('#48bb78');
		else if (bucket < 100) bgColors.push('#68d391');
		else if (bucket < 200) bgColors.push('#ed8936');
		else bgColors.push('#e53e3e');
	});
	
	charts.distribution = new Chart(ctx, {
		type: 'bar',
		data: {
			labels: labels,
			datasets: [{
				label: 'Count',
				data: values,
				backgroundColor: bgColors,
				borderRadius: 4
			}]
		},
		options: {
			responsive: true,
			maintainAspectRatio: false,
			plugins: {
				legend: { display: false },
				tooltip: { backgroundColor: '#1e1e1e', titleColor: '#e0e0e0', bodyColor: '#b0b0b0' }
			},
			scales: {
				x: { grid: { color: '#404040' }, ticks: { color: '#b0b0b0', font: { size: 9 } } },
				y: { beginAtZero: true, grid: { color: '#404040' }, ticks: { color: '#b0b0b0' }, title: { display: true, text: 'Count', color: '#b0b0b0' } }
			}
		}
	});
}

// Create jitter chart
function createJitterChart(data) {
	destroyChart('jitter');
	var ctx = document.getElementById('jitterChart');
	if (!ctx) return;
	
	// Calculate jitter per 5-minute window
	var buckets = {};
	
	data.targets.forEach(function(target) {
		if (!targetMatchesFilter(target)) return;
		
		target.points.forEach(function(p) {
			if (!p.success) return;
			var ts = new Date(p.timestamp);
			var bucketKey = Math.floor(ts.getTime() / (5 * 60 * 1000)) * (5 * 60 * 1000);
			if (!buckets[bucketKey]) buckets[bucketKey] = [];
			buckets[bucketKey].push(p.latency_ms);
		});
	});
	
	var chartData = [];
	Object.keys(buckets).sort().forEach(function(key) {
		var ts = new Date(parseInt(key));
		var jitter = standardDeviation(buckets[key]);
		chartData.push({ x: ts, y: jitter });
	});
	
	var options = getBaseChartOptions();
	options.scales.y.title = { display: true, text: 'Jitter (ms)', color: '#b0b0b0' };
	
	charts.jitter = new Chart(ctx, {
		type: 'line',
		data: {
			datasets: [{
				label: 'Jitter',
				data: chartData,
				borderColor: '#9f7aea',
				backgroundColor: '#9f7aea40',
				borderWidth: 2,
				pointRadius: 0,
				fill: true,
				tension: 0.3
			}]
		},
		options: options
	});
}

// Create hourly pattern chart
function createHourlyPatternChart(data) {
	destroyChart('hourlyPattern');
	var ctx = document.getElementById('hourlyPatternChart');
	if (!ctx) return;
	
	// Group by hour of day
	var hourlyData = {};
	for (var i = 0; i < 24; i++) hourlyData[i] = [];
	
	data.targets.forEach(function(target) {
		if (!targetMatchesFilter(target)) return;
		
		target.points.forEach(function(p) {
			if (!p.success) return;
			var hour = new Date(p.timestamp).getHours();
			hourlyData[hour].push(p.latency_ms);
		});
	});
	
	var labels = [];
	var avgValues = [];
	var p95Values = [];
	
	for (var h = 0; h < 24; h++) {
		labels.push(h.toString().padStart(2, '0') + ':00');
		if (hourlyData[h].length > 0) {
			var sum = hourlyData[h].reduce(function(a, b) { return a + b; }, 0);
			avgValues.push(sum / hourlyData[h].length);
			p95Values.push(percentile(hourlyData[h], 95));
		} else {
			avgValues.push(null);
			p95Values.push(null);
		}
	}
	
	charts.hourlyPattern = new Chart(ctx, {
		type: 'bar',
		data: {
			labels: labels,
			datasets: [
				{ label: 'Avg Latency', data: avgValues, backgroundColor: '#667eea', borderRadius: 4 },
				{ label: 'P95 Latency', data: p95Values, backgroundColor: '#ed8936', borderRadius: 4 }
			]
		},
		options: {
			responsive: true,
			maintainAspectRatio: false,
			plugins: {
				legend: { display: true, position: 'bottom', labels: { color: '#b0b0b0', font: { size: 10 } } },
				tooltip: { backgroundColor: '#1e1e1e', titleColor: '#e0e0e0', bodyColor: '#b0b0b0' }
			},
			scales: {
				x: { grid: { color: '#404040' }, ticks: { color: '#b0b0b0', font: { size: 9 } } },
				y: { beginAtZero: true, grid: { color: '#404040' }, ticks: { color: '#b0b0b0' }, title: { display: true, text: 'Latency (ms)', color: '#b0b0b0' } }
			}
		}
	});
}

// Create comparison chart
function createComparisonChart(data) {
	destroyChart('comparison');
	var ctx = document.getElementById('comparisonChart');
	if (!ctx) return;
	
	var labels = [];
	var avgLatencies = [];
	var uptimes = [];
	var bgColors = [];
	
	data.targets.forEach(function(target, index) {
		if (!targetMatchesFilter(target)) return;
		
		var successCount = 0;
		var latencySum = 0;
		var latencyCount = 0;
		
		target.points.forEach(function(p) {
			if (p.success) {
				successCount++;
				latencySum += p.latency_ms;
				latencyCount++;
			}
		});
		
		var label = formatTargetLabel(target);
		labels.push(label.length > 20 ? label.substring(0, 20) + '...' : label);
		avgLatencies.push(latencyCount > 0 ? latencySum / latencyCount : 0);
		uptimes.push(target.points.length > 0 ? (successCount / target.points.length) * 100 : 0);
		bgColors.push(colors[index % colors.length]);
	});
	
	charts.comparison = new Chart(ctx, {
		type: 'bar',
		data: {
			labels: labels,
			datasets: [
				{ label: 'Avg Latency (ms)', data: avgLatencies, backgroundColor: '#667eea', borderRadius: 4, yAxisID: 'y' },
				{ label: 'Uptime (%)', data: uptimes, backgroundColor: '#48bb78', borderRadius: 4, yAxisID: 'y1' }
			]
		},
		options: {
			responsive: true,
			maintainAspectRatio: false,
			indexAxis: 'y',
			plugins: {
				legend: { display: true, position: 'bottom', labels: { color: '#b0b0b0', font: { size: 10 } } },
				tooltip: { backgroundColor: '#1e1e1e', titleColor: '#e0e0e0', bodyColor: '#b0b0b0' }
			},
			scales: {
				x: { beginAtZero: true, grid: { color: '#404040' }, ticks: { color: '#b0b0b0' } },
				y: { grid: { color: '#404040' }, ticks: { color: '#b0b0b0', font: { size: 10 } } },
				y1: { position: 'right', beginAtZero: true, max: 100, grid: { display: false }, ticks: { color: '#48bb78' } }
			}
		}
	});
}

// Create heatmap chart (using matrix-like visualization)
function createHeatmapChart(data) {
	destroyChart('heatmap');
	var ctx = document.getElementById('heatmapChart');
	if (!ctx) return;
	
	// Build heatmap data - targets x time buckets
	var targets = [];
	var timeLabels = [];
	var heatmapData = [];
	
	// 30-minute time buckets
	var bucketSize = 30 * 60 * 1000;
	var now = Date.now();
	var hoursSelect = document.getElementById('hoursSelect');
	var hours = hoursSelect ? parseInt(hoursSelect.value) : 24;
	var startTime = now - (hours * 60 * 60 * 1000);
	
	// Generate time labels
	for (var t = startTime; t < now; t += bucketSize) {
		timeLabels.push(new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false }));
	}
	
	data.targets.forEach(function(target) {
		if (!targetMatchesFilter(target)) return;
		
		var label = formatTargetLabel(target);
		targets.push(label.length > 15 ? label.substring(0, 15) + '...' : label);
		
		var buckets = {};
		target.points.forEach(function(p) {
			if (!p.success) return;
			var bucketKey = Math.floor(new Date(p.timestamp).getTime() / bucketSize) * bucketSize;
			if (!buckets[bucketKey]) buckets[bucketKey] = [];
			buckets[bucketKey].push(p.latency_ms);
		});
		
		var rowData = [];
		for (var t = startTime; t < now; t += bucketSize) {
			var key = Math.floor(t / bucketSize) * bucketSize;
			if (buckets[key] && buckets[key].length > 0) {
				var avg = buckets[key].reduce(function(a, b) { return a + b; }, 0) / buckets[key].length;
				rowData.push(avg);
			} else {
				rowData.push(null);
			}
		}
		heatmapData.push(rowData);
	});
	
	if (targets.length === 0) return;
	
	// Create datasets for stacked bar (simulating heatmap)
	var datasets = [];
	targets.forEach(function(targetName, targetIndex) {
		var data = heatmapData[targetIndex].map(function(val, timeIndex) {
			return val !== null ? { x: timeLabels[timeIndex], y: targetName, v: val } : null;
		}).filter(function(d) { return d !== null; });
		
		// Create color based on latency
		datasets.push({
			label: targetName,
			data: heatmapData[targetIndex],
			backgroundColor: heatmapData[targetIndex].map(function(val) {
				if (val === null) return '#333';
				if (val < 50) return '#48bb78';
				if (val < 100) return '#68d391';
				if (val < 150) return '#ed8936';
				if (val < 200) return '#f56565';
				return '#e53e3e';
			}),
			borderWidth: 1,
			borderColor: '#1e1e1e'
		});
	});
	
	charts.heatmap = new Chart(ctx, {
		type: 'bar',
		data: {
			labels: timeLabels,
			datasets: datasets
		},
		options: {
			responsive: true,
			maintainAspectRatio: false,
			plugins: {
				legend: { display: true, position: 'bottom', labels: { color: '#b0b0b0', font: { size: 9 }, boxWidth: 10 } },
				tooltip: {
					backgroundColor: '#1e1e1e',
					callbacks: {
						label: function(context) {
							var val = context.raw;
							return context.dataset.label + ': ' + (val !== null ? val.toFixed(1) + 'ms' : 'No data');
						}
					}
				}
			},
			scales: {
				x: { stacked: true, grid: { color: '#404040' }, ticks: { color: '#b0b0b0', font: { size: 8 }, maxRotation: 45 } },
				y: { stacked: true, grid: { color: '#404040' }, ticks: { color: '#b0b0b0' } }
			}
		}
	});
}

// Update all charts
function updateCharts(data) {
	// Show loading bar during chart updates
	showLoading();
	
	// Use requestAnimationFrame to ensure loading bar is visible
	requestAnimationFrame(function() {
		// Count matches
		var matchCount = 0;
		var totalPoints = 0;
		
		data.targets.forEach(function(target) {
			if (targetMatchesFilter(target)) {
				matchCount++;
				totalPoints += target.points.length;
			}
		});
		
		updateMatchInfo(matchCount, data.targets.length);
		
		// Show/hide no data message
		var noDataMsg = document.getElementById('noDataMessage');
		var containers = ['latencyChartContainer', 'packetLossChartContainer', 'percentilesChartContainer',
			'successRateChartContainer', 'distributionChartContainer', 'jitterChartContainer',
			'hourlyPatternChartContainer', 'comparisonChartContainer', 'heatmapChartContainer'];
		
		if (totalPoints === 0) {
			if (noDataMsg) noDataMsg.style.display = 'block';
			containers.forEach(function(id) {
				var el = document.getElementById(id);
				if (el) el.style.display = 'none';
			});
			updateStats(data);
			hideLoading();
			return;
		} else {
			if (noDataMsg) noDataMsg.style.display = 'none';
			containers.forEach(function(id) {
				var el = document.getElementById(id);
				if (el) el.style.display = 'block';
			});
		}
		
		// Create all charts
		createLatencyChart(data);
		createPacketLossChart(data);
		createPercentilesChart(data);
		createSuccessRateChart(data);
		createDistributionChart(data);
		createJitterChart(data);
		createHourlyPatternChart(data);
		createComparisonChart(data);
		createHeatmapChart(data);
		
		updateStats(data);
		
		// Hide loading bar after charts are updated
		hideLoading();
	});
}

// Refresh data and update charts
async function refreshData() {
	showLoading();
	try {
		var data = await fetchData();
		if (data) {
			updateCharts(data);
		}
	} finally {
		// Hide loading bar immediately after charts update
		hideLoading();
	}
}

// Initialize when DOM is ready
document.addEventListener('DOMContentLoaded', function() {
	var hoursSelect = document.getElementById('hoursSelect');
	var targetSelect = document.getElementById('targetSelect');
	var refreshBtn = document.getElementById('refreshBtn');
	var searchInput = document.getElementById('searchInput');
	var clearBtn = document.getElementById('clearBtn');
	
	if (hoursSelect) {
		hoursSelect.addEventListener('change', refreshData);
	}
	
	if (targetSelect) {
		targetSelect.addEventListener('change', function() {
			if (this.value !== 'all' && searchInput) {
				searchInput.value = '';
			}
			if (cachedData) {
				updateCharts(cachedData);
			}
		});
	}
	
	if (refreshBtn) {
		refreshBtn.addEventListener('click', refreshData);
	}
	
	if (searchInput) {
		var searchTimeout = null;
		searchInput.addEventListener('input', function() {
			if (targetSelect) targetSelect.value = 'all';
			
			// Clear previous timeout
			clearTimeout(searchTimeout);
			
			// Debounce chart updates for better performance
			searchTimeout = setTimeout(function() {
				if (cachedData) {
					updateCharts(cachedData);
				}
			}, 150); // Small delay to batch rapid typing
		});
	}
	
	if (clearBtn) {
		clearBtn.addEventListener('click', function() {
			if (searchInput) searchInput.value = '';
			if (targetSelect) targetSelect.value = 'all';
			if (cachedData) {
				updateCharts(cachedData);
			}
		});
	}
	
	// Reset zoom button handlers
	var resetZoomBtns = document.querySelectorAll('.reset-zoom-btn');
	resetZoomBtns.forEach(function(btn) {
		btn.addEventListener('click', function() {
			var chartName = this.getAttribute('data-chart');
			if (chartName && charts[chartName]) {
				charts[chartName].resetZoom();
			}
		});
	});
	
	// Export CSV button handler
	var exportCSVBtn = document.getElementById('exportCSVBtn');
	if (exportCSVBtn) {
		exportCSVBtn.addEventListener('click', function() {
			if (cachedData) {
				exportToCSV(cachedData);
			} else {
				alert('No data available to export. Please wait for data to load.');
			}
		});
	}
	
	// Export JSON button handler
	var exportJSONBtn = document.getElementById('exportJSONBtn');
	if (exportJSONBtn) {
		exportJSONBtn.addEventListener('click', function() {
			if (cachedData) {
				exportToJSON(cachedData);
			} else {
				alert('No data available to export. Please wait for data to load.');
			}
		});
	}
	
	// Initial load - defer slightly to allow page to render first
	setTimeout(function() {
		refreshData();
	}, 100);
	
	// Auto-refresh every 60 seconds
	setInterval(refreshData, 60000);
	
	// Scroll to top button functionality
	function initScrollToTop() {
		const scrollToTopBtn = document.getElementById('scrollToTop');
		if (scrollToTopBtn) {
			window.addEventListener('scroll', function() {
				if (window.pageYOffset > 300) {
					scrollToTopBtn.classList.add('show');
				} else {
					scrollToTopBtn.classList.remove('show');
				}
			});
			scrollToTopBtn.addEventListener('click', function() {
				window.scrollTo({
					top: 0,
					behavior: 'smooth'
				});
			});
		}
	}
	initScrollToTop();
});
