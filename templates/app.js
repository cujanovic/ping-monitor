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

// Initialize first report as expanded
window.addEventListener('DOMContentLoaded', function() {
	const firstIcon = document.getElementById('icon-0');
	if (firstIcon) {
		firstIcon.textContent = '▲';
		firstIcon.classList.add('rotated');
	}
});

