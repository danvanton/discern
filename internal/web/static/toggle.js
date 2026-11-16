// Disables a group of choices while the switch that enables it is off: a
// fieldset with data-enabled-by="ID" is disabled unless the checkbox or
// radio with that id is checked. Used by the Settings page (the language
// choices, while MakeMKV's default selection is on) and the review page
// (this disc's languages, until "Choose languages for this disc" is ticked).
//
// A disabled fieldset disables every control inside it, for the mouse, the
// keyboard and screen readers alike, and its controls aren't sent with the
// form. Without this script the pages still work: style.css dims the group,
// and the server ignores it while its switch is off.
(function () {
  document.querySelectorAll("fieldset[data-enabled-by]").forEach(function (box) {
    var on = document.getElementById(box.getAttribute("data-enabled-by"));
    if (!on) {
      return;
    }
    // For a radio, choosing another in its group turns it off without a
    // change event of its own, so the whole group is watched.
    var group = on.type === "radio" ? document.getElementsByName(on.name) : [on];
    function sync() {
      box.disabled = !on.checked;
    }
    Array.prototype.forEach.call(group, function (input) {
      input.addEventListener("change", sync);
    });
    sync();
  });
})();
