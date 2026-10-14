const portalError = (message) => {
  const alert = document.getElementById("portal-error");
  if (alert) {
    alert.textContent = message;
    alert.hidden = !message;
  }
};
document.addEventListener("htmx:before:request", () => portalError(""));
document.addEventListener("htmx:response:error", () => {
  portalError("Unable to load the requested data. The displayed data may be out of date. Try again or reload the page.");
});
document.addEventListener("htmx:error", () => {
  portalError("Unable to connect. Check your connection and try again.");
});
document.addEventListener("htmx:after:swap", () => {
  document.getElementById("workspace")?.focus({ preventScroll: true });
});
