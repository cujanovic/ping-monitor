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
	const filter = searchInput.value.toLowerCase().trim();
	const incidents = document.querySelectorAll('.incident-item');
	const countDiv = document.getElementById('incidentCount');
	let visibleCount = 0;
	
	incidents.forEach(function(incident) {
		const searchText = incident.getAttribute('data-search-text').toLowerCase();
		
		if (filter === '' || searchText.includes(filter)) {
			incident.style.display = '';
			visibleCount++;
		} else {
			incident.style.display = 'none';
		}
	});
	
	// Update counter
	document.getElementById('matchCount').textContent = visibleCount;
	
	// Show/hide counter based on search activity
	if (filter === '') {
		countDiv.style.display = 'none';
	} else {
		countDiv.style.display = 'block';
	}
	
	// Show "no results" message if needed
	const incidentsList = document.getElementById('incidentsList');
	let noResultsMsg = document.getElementById('noResultsMsg');
	
	if (visibleCount === 0 && filter !== '') {
		if (!noResultsMsg) {
			noResultsMsg = document.createElement('div');
			noResultsMsg.id = 'noResultsMsg';
			noResultsMsg.style.cssText = 'padding: 30px; text-align: center; color: #999; font-size: 14px;';
			noResultsMsg.innerHTML = '🔍 No incidents found matching "<span style="color: #667eea;">' + filter + '</span>"';
			incidentsList.appendChild(noResultsMsg);
		} else {
			noResultsMsg.innerHTML = '🔍 No incidents found matching "<span style="color: #667eea;">' + filter + '</span>"';
			noResultsMsg.style.display = 'block';
		}
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
});

