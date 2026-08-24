// Theme is remembered per browser. With nothing stored the terminal's own
// setting wins, which is what a tool that adapts to the host terminal should
// do on the web too.
(function () {
  var sun =
    '<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>';
  var moon =
    '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/></svg>';

  function stored() {
    try {
      return localStorage.getItem("theme");
    } catch (error) {
      // Private windows and blocked site data throw rather than return null.
      return null;
    }
  }

  function remember(theme) {
    try {
      localStorage.setItem("theme", theme);
    } catch (error) {
      // Not being able to remember is not a reason to refuse to switch.
    }
  }

  function preferred() {
    return window.matchMedia &&
      window.matchMedia("(prefers-color-scheme: light)").matches
      ? "light"
      : "dark";
  }

  function apply(theme) {
    document.documentElement.setAttribute("data-theme", theme);
    var button = document.querySelector(".theme-toggle");
    if (button) {
      button.innerHTML = theme === "light" ? moon : sun;
      button.setAttribute(
        "aria-label",
        theme === "light" ? "Switch to dark theme" : "Switch to light theme"
      );
    }
  }

  window.toggleTheme = function () {
    var next =
      document.documentElement.getAttribute("data-theme") === "light"
        ? "dark"
        : "light";
    remember(next);
    apply(next);
  };

  apply(stored() || preferred());
  document.addEventListener("DOMContentLoaded", function () {
    apply(stored() || preferred());

    document.querySelectorAll("[data-copy]").forEach(function (button) {
      button.addEventListener("click", function () {
        var text = button.getAttribute("data-copy");
        var original = button.textContent;
        function restore() {
          setTimeout(function () {
            button.textContent = original;
          }, 1400);
        }
        if (!navigator.clipboard) {
          button.textContent = "select it";
          restore();
          return;
        }
        // Permissions policy, an insecure context, and Safari's own rules all
        // reject this. Saying nothing would look like the button is broken.
        navigator.clipboard.writeText(text).then(
          function () {
            button.textContent = "copied";
            restore();
          },
          function () {
            button.textContent = "select it";
            restore();
          }
        );
      });
    });
  });
})();
