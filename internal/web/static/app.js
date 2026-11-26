// Copy buttons: one for a whole Markdown source, and one on each code block
// of a rendered document.
function setUpCopy(button, readText) {
  var label = button.textContent;
  var reset;
  button.addEventListener("click", async function () {
    var delay = 1500;
    try {
      await navigator.clipboard.writeText(readText());
      button.textContent = "コピーしました";
    } catch {
      button.textContent = "コピーできませんでした";
      delay = 2500;
    }
    clearTimeout(reset);
    reset = setTimeout(function () { button.textContent = label; }, delay);
  });
}
document.querySelectorAll("button.copy[data-copy]").forEach(function (button) {
  setUpCopy(button, function () {
    return document.querySelector(button.dataset.copy).textContent;
  });
});
document.querySelectorAll(".markdown pre").forEach(function (pre) {
  var button = document.createElement("button");
  button.type = "button";
  button.className = "copy on-code";
  button.textContent = "コピー";
  setUpCopy(button, function () {
    return (pre.querySelector("code") || pre).textContent;
  });
  var box = document.createElement("div");
  box.className = "code";
  pre.replaceWith(box);
  box.append(button, pre);
});
