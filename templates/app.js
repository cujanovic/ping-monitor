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
});

