// Settings page: the language choices do nothing while "MakeMKV's default
// selection" is on, so they are disabled then. A disabled fieldset disables
// every control inside it, for the mouse, the keyboard and screen readers
// alike, and its controls aren't sent with the form.
//
// Without this script the page still works: the choices are only dimmed (see
// style.css), and the server ignores them while the default is on.
(function () {
  var box = document.getElementById("chosen-languages");
  var radios = document.querySelectorAll('input[name="languages"]');
  if (!box || radios.length === 0) {
    return;
  }
  function sync() {
    var on = document.querySelector('input[name="languages"]:checked');
    box.disabled = !on || on.value !== "chosen";
  }
  radios.forEach(function (radio) {
    radio.addEventListener("change", sync);
  });
  sync();
})();
