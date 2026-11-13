// The expired-link page's only script: a button that copies the command.
// The page works without it; the button stays hidden until this runs.
(function () {
  "use strict";
  document.addEventListener("DOMContentLoaded", function () {
    var row = document.getElementById("copy-row");
    var button = document.getElementById("copy");
    var said = document.getElementById("copied");
    var command = document.getElementById("command");
    if (!row || !button || !said || !command || !navigator.clipboard || !navigator.clipboard.writeText) return;
    row.hidden = false;
    button.addEventListener("click", function () {
      navigator.clipboard.writeText(command.textContent).then(function () {
        said.textContent = "Copied. Paste it in a terminal.";
      }, function () {
        said.textContent = "Could not copy. Select the command instead.";
      });
    });
  });
})();
