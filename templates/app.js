// Toggle report visibility
function toggleReport(index) {
	const content = document.getElementById('report-' + index);
	const icon = document.getElementById('icon-' + index);
	
	if (content.classList.contains('active')) {
		content.classList.remove('active');
		icon.textContent = '▼';
		icon.classList.remove('rotated');
	} else {
		content.classList.add('active');
		icon.textContent = '▲';
		icon.classList.add('rotated');
	}
}

// Filter incidents based on search input
function filterIncidents() {
	const searchInput = document.getElementById('incidentSearch');
	if (!searchInput) return;
	
	const filter = searchInput.value.toLowerCase().trim();
	const incidents = document.querySelectorAll('.incident-item');
	const countDiv = document.getElementById('incidentCount');
	let visibleCount = 0;
	
	incidents.forEach(function(incident) {
		// Use textContent instead of data attribute to avoid XSS concerns
		// This searches the visible text only (target name, description, timestamp, status)
		const searchText = incident.textContent || incident.innerText || '';
		const searchTextLower = searchText.toLowerCase();
		
		if (filter === '' || searchTextLower.includes(filter)) {
			incident.style.display = '';
			visibleCount++;
		} else {
			incident.style.display = 'none';
		}
	});
	
	// Update counter
	const matchCountEl = document.getElementById('matchCount');
	if (matchCountEl) matchCountEl.textContent = visibleCount;
	
	// Show/hide counter based on search activity
	if (countDiv) {
		if (filter === '') {
			countDiv.style.display = 'none';
		} else {
			countDiv.style.display = 'block';
		}
	}
	
	// Show "no results" message if needed
	const incidentsList = document.getElementById('incidentsList');
	let noResultsMsg = document.getElementById('noResultsMsg');
	
	if (visibleCount === 0 && filter !== '') {
		if (!noResultsMsg) {
			noResultsMsg = document.createElement('div');
			noResultsMsg.id = 'noResultsMsg';
			noResultsMsg.style.cssText = 'padding: 30px; text-align: center; color: #999; font-size: 14px;';
			if (incidentsList) incidentsList.appendChild(noResultsMsg);
		}
		
		// Safely construct the message without innerHTML to prevent XSS
		noResultsMsg.textContent = '';
		noResultsMsg.appendChild(document.createTextNode('🔍 No incidents found matching "'));
		
		const filterSpan = document.createElement('span');
		filterSpan.style.color = '#667eea';
		filterSpan.textContent = filter; // Safe: uses textContent instead of innerHTML
		noResultsMsg.appendChild(filterSpan);
		
		noResultsMsg.appendChild(document.createTextNode('"'));
		noResultsMsg.style.display = 'block';
	} else if (noResultsMsg) {
		noResultsMsg.style.display = 'none';
	}
}

// Initialize when DOM is ready
window.addEventListener('DOMContentLoaded', function() {
	// Attach click handlers to all report headers
	const reportHeaders = document.querySelectorAll('.report-header[data-report-index]');
	reportHeaders.forEach(function(header) {
		header.addEventListener('click', function() {
			const index = this.getAttribute('data-report-index');
			toggleReport(index);
		});
		
		// Set cursor pointer for better UX
		header.style.cursor = 'pointer';
	});
	
	// Initialize first report as expanded
	const firstIcon = document.getElementById('icon-0');
	if (firstIcon) {
		firstIcon.textContent = '▲';
		firstIcon.classList.add('rotated');
	}
	
	// Initialize incident search counts
	const incidents = document.querySelectorAll('.incident-item');
	if (incidents.length > 0) {
		const totalCount = incidents.length;
		const totalCountEl = document.getElementById('totalCount');
		const matchCountEl = document.getElementById('matchCount');
		if (totalCountEl) totalCountEl.textContent = totalCount;
		if (matchCountEl) matchCountEl.textContent = totalCount;
	}
	
	// Attach search input event listeners
	const searchInput = document.getElementById('incidentSearch');
	if (searchInput) {
		// Keyup event for filtering
		searchInput.addEventListener('keyup', filterIncidents);
		
		// Focus event - change border color
		searchInput.addEventListener('focus', function() {
			this.style.borderColor = '#667eea';
		});
		
		// Blur event - restore border color
		searchInput.addEventListener('blur', function() {
			this.style.borderColor = '#444';
		});
	}
	
	// Attach event listeners to disable/enable buttons (using event delegation for security)
	document.addEventListener('click', function(e) {
		if (e.target.classList.contains('btn-disable-target')) {
			const targetAddr = e.target.getAttribute('data-target-addr');
			const targetName = e.target.getAttribute('data-target-name');
			if (targetAddr && targetName) {
				disableTarget(targetAddr, targetName);
			}
		} else if (e.target.classList.contains('btn-enable-target')) {
			const targetAddr = e.target.getAttribute('data-target-addr');
			const targetName = e.target.getAttribute('data-target-name');
			if (targetAddr && targetName) {
				enableTarget(targetAddr, targetName);
			}
		} else if (e.target.id === 'btn-disable-all') {
			disableAllTargets();
		} else if (e.target.id === 'btn-enable-all') {
			enableAllTargets();
		}
	});
});

// Disable a target from monitoring
function disableTarget(targetAddr, targetName) {
	if (!confirm(`Are you sure you want to disable monitoring for "${targetName}"?\n\nThis will stop all ping checks for this target until you re-enable it.`)) {
		return;
	}
	
	const formData = new FormData();
	formData.append('target', targetAddr);
	
	fetch('/api/target/disable', {
		method: 'POST',
		body: formData
	})
	.then(response => response.json())
	.then(data => {
		if (data.success) {
			// Reload the page to show updated status
			window.location.reload();
		} else {
			alert('Failed to disable target: ' + (data.error || 'Unknown error'));
		}
	})
	.catch(error => {
		console.error('Error disabling target:', error);
		alert('Error disabling target: ' + error.message);
	});
}

// Enable a target for monitoring
function enableTarget(targetAddr, targetName) {
	const formData = new FormData();
	formData.append('target', targetAddr);
	
	fetch('/api/target/enable', {
		method: 'POST',
		body: formData
	})
	.then(response => response.json())
	.then(data => {
		if (data.success) {
			// Reload the page to show updated status
			window.location.reload();
		} else {
			alert('Failed to enable target: ' + (data.error || 'Unknown error'));
		}
	})
	.catch(error => {
		console.error('Error enabling target:', error);
		alert('Error enabling target: ' + error.message);
	});
}

// Disable all targets
function disableAllTargets() {
	if (!confirm('Are you sure you want to disable monitoring for ALL targets?\n\nThis will stop all ping checks for all targets until you re-enable them.')) {
		return;
	}
	
	fetch('/api/target/disable-all', {
		method: 'POST'
	})
	.then(response => {
		// Handle authentication errors - redirect to login
		if (response.status === 401) {
			window.location.href = '/login?return=' + encodeURIComponent(window.location.pathname);
			return null;
		}
		if (!response.ok) {
			throw new Error('HTTP ' + response.status);
		}
		return response.json();
	})
	.then(data => {
		if (!data) return; // Auth redirect handled above
		if (data.success) {
			// Reload the page to show updated status
			window.location.reload();
		} else {
			alert('Failed to disable all targets: ' + (data.error || 'Unknown error'));
		}
	})
	.catch(error => {
		console.error('Error disabling all targets:', error);
		alert('Error disabling all targets: ' + error.message);
	});
}

// Enable all targets
function enableAllTargets() {
	if (!confirm('Are you sure you want to enable monitoring for ALL targets?\n\nThis will resume ping checks for all targets.')) {
		return;
	}
	
	fetch('/api/target/enable-all', {
		method: 'POST'
	})
	.then(response => {
		// Handle authentication errors - redirect to login
		if (response.status === 401) {
			window.location.href = '/login?return=' + encodeURIComponent(window.location.pathname);
			return null;
		}
		if (!response.ok) {
			throw new Error('HTTP ' + response.status);
		}
		return response.json();
	})
	.then(data => {
		if (!data) return; // Auth redirect handled above
		if (data.success) {
			// Reload the page to show updated status
			window.location.reload();
		} else {
			alert('Failed to enable all targets: ' + (data.error || 'Unknown error'));
		}
	})
	.catch(error => {
		console.error('Error enabling all targets:', error);
		alert('Error enabling all targets: ' + error.message);
	});
}

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

// Initialize scroll to top when DOM is ready
if (document.readyState === 'loading') {
	document.addEventListener('DOMContentLoaded', initScrollToTop);
} else {
	initScrollToTop();
}

