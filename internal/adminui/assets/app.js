"use strict";

for (const button of document.querySelectorAll("[data-copy-target]")) {
  button.addEventListener("click", async () => {
    const input = document.getElementById(button.dataset.copyTarget);
    const status = document.getElementById("copy-status");
    if (!(input instanceof HTMLInputElement)) return;

    try {
      await navigator.clipboard.writeText(input.value);
      status.textContent = "Copied to clipboard.";
    } catch (_) {
      input.focus();
      input.select();
      status.textContent = "Copy is unavailable. The key is selected; use your system copy command.";
    }
  });
}
