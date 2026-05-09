import '@testing-library/jest-dom'

// jsdom does not implement scrollIntoView — mock it globally so component
// auto-scroll effects don't throw in tests.
window.HTMLElement.prototype.scrollIntoView = () => {}
