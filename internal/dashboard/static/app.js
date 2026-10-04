// Zweep dashboard: confirmation before destructive forms (no inline scripts, CSP 'self')
document.addEventListener("submit", function (e) {
  // The button that submitted may carry its own question (forms with more than one action)
  var msg = (e.submitter && e.submitter.getAttribute("data-confirm")) || e.target.getAttribute("data-confirm");
  if (msg && !window.confirm(msg)) {
    e.preventDefault();
  }
});

// Close an open menu when clicking elsewhere or pressing Escape
document.addEventListener("click", function (e) {
  document.querySelectorAll("details.menu[open]").forEach(function (d) {
    if (!d.contains(e.target)) {
      d.removeAttribute("open");
    }
  });
});
document.addEventListener("keydown", function (e) {
  if (e.key === "Escape") {
    document.querySelectorAll("details.menu[open]").forEach(function (d) { d.removeAttribute("open"); });
  }
});

// Focus the first field with an error
document.addEventListener("DOMContentLoaded", function () {
  var bad = document.querySelector("[aria-invalid=true]");
  if (bad) {
    bad.focus();
  }
});

// Group picker: search field, filtered list, chosen groups as removable chips
(function () {
  function escapeHTML(s) {
    return s.replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }
  function setup(picker) {
    var input = picker.querySelector(".picker-input");
    var list = picker.querySelector(".picker-list");
    var picked = picker.querySelector(".picked");
    var empty = list.querySelector(".picker-empty");
    var options = Array.prototype.slice.call(list.querySelectorAll("li[data-value]"));
    var field = picker.getAttribute("data-name") || "hostgroups";
    var active = -1;
    function chosen(value) {
      return !!picked.querySelector('.gchip[data-value="' + CSS.escape(value) + '"]');
    }
    function visible() {
      return options.filter(function (o) { return !o.hidden; });
    }
    function render() {
      var q = input.value.trim().toLowerCase();
      var shown = 0;
      options.forEach(function (o) {
        var name = o.getAttribute("data-value");
        var at = name.toLowerCase().indexOf(q);
        var match = q === "" || at >= 0;
        o.hidden = !match;
        o.setAttribute("aria-selected", chosen(name) ? "true" : "false");
        var label = o.querySelector(".gname");
        label.innerHTML = q === "" || at < 0 ? escapeHTML(name)
          : escapeHTML(name.slice(0, at)) + "<mark>" + escapeHTML(name.slice(at, at + q.length)) + "</mark>" + escapeHTML(name.slice(at + q.length));
        if (match) {
          shown++;
        }
      });
      empty.hidden = shown > 0;
      setActive(shown > 0 && q !== "" ? 0 : -1);
    }
    function setActive(i) {
      var v = visible();
      options.forEach(function (o) { o.classList.remove("active"); });
      active = i;
      if (i >= 0 && v[i]) {
        v[i].classList.add("active");
        v[i].scrollIntoView({ block: "nearest" });
      }
    }
    function open() {
      list.hidden = false;
      input.setAttribute("aria-expanded", "true");
      render();
    }
    function close() {
      list.hidden = true;
      input.setAttribute("aria-expanded", "false");
    }
    function addChip(value) {
      var chip = document.createElement("span");
      chip.className = "gchip";
      chip.setAttribute("data-value", value);
      chip.innerHTML = '<span class="gchip-name"></span><input type="hidden"><button type="button" class="gchip-x">×</button>';
      chip.querySelector("input").name = field;
      chip.querySelector(".gchip-name").textContent = value;
      chip.querySelector("input").value = value;
      chip.querySelector("button").setAttribute("aria-label", value);
      picked.appendChild(chip);
    }
    function toggle(value) {
      var chip = picked.querySelector('.gchip[data-value="' + CSS.escape(value) + '"]');
      if (chip) {
        chip.remove();
      } else {
        addChip(value);
      }
      render();
    }
    input.addEventListener("focus", open);
    input.addEventListener("click", open);
    input.addEventListener("input", function () {
      list.hidden = false;
      render();
    });
    input.addEventListener("keydown", function (e) {
      var v = visible();
      if (e.key === "ArrowDown") {
        e.preventDefault();
        if (list.hidden) {
          open();
        }
        setActive(Math.min(active + 1, v.length - 1));
      } else if (e.key === "ArrowUp") {
        e.preventDefault();
        setActive(Math.max(active - 1, 0));
      } else if (e.key === "Enter") {
        e.preventDefault(); // never submit the form from the search field
        if (active >= 0 && v[active] && !v[active].classList.contains("locked")) {
          toggle(v[active].getAttribute("data-value"));
          input.value = "";
          render();
        }
      } else if (e.key === "Escape") {
        close();
      } else if (e.key === "Backspace" && input.value === "" && picked.lastElementChild) {
        picked.lastElementChild.remove();
        render();
      }
    });
    list.addEventListener("mousedown", function (e) {
      e.preventDefault(); // keep the focus in the search field
      var li = e.target.closest("li[data-value]");
      if (li && !li.classList.contains("locked")) { // inherited from a user group: changed in the group
        toggle(li.getAttribute("data-value"));
      }
    });
    picked.addEventListener("click", function (e) {
      if (e.target.classList.contains("gchip-x")) {
        e.target.parentNode.remove();
        render();
      }
    });
    document.addEventListener("click", function (e) {
      if (!picker.contains(e.target)) {
        close();
      }
    });
  }
  document.addEventListener("DOMContentLoaded", function () {
    Array.prototype.forEach.call(document.querySelectorAll(".picker"), setup);
  });
})();

// Fields that apply to one choice of a select (Test page: recipients)
document.addEventListener("DOMContentLoaded", function () {
  // data-for="select:value" (or "select:v1|v2"): shown only for those values of the named select
  document.querySelectorAll("form").forEach(function (form) {
    var selects = form.querySelectorAll("select[data-show-for]");
    if (!selects.length) {
      return;
    }
    function update() {
      form.querySelectorAll("[data-for]").forEach(function (el) {
        var spec = el.getAttribute("data-for").split(":");
        var sel = form.querySelector('select[name="' + spec[0] + '"]');
        el.hidden = !sel || spec[1].split("|").indexOf(sel.value) < 0;
      });
    }
    selects.forEach(function (sel) { sel.addEventListener("change", update); });
    update();
  });
});

// Logging page: scroll to the newest line; live view with server-sent events
document.addEventListener("DOMContentLoaded", function () {
  var view = document.getElementById("logview");
  if (!view) {
    return;
  }
  view.scrollTop = view.scrollHeight;
  var url = view.getAttribute("data-live");
  if (!url || !window.EventSource) {
    return;
  }
  var max = parseInt(view.getAttribute("data-max"), 10) || 500;
  var pause = document.getElementById("log-pause");
  var state = document.getElementById("log-state");
  var count = document.getElementById("log-count");
  var paused = false;
  pause.hidden = false;
  pause.addEventListener("click", function () {
    paused = !paused;
    pause.textContent = pause.getAttribute(paused ? "data-resume" : "data-pause");
  });
  var src = new EventSource(url);
  src.onmessage = function (ev) {
    if (paused) {
      return;
    }
    var line = JSON.parse(ev.data);
    var atBottom = view.scrollHeight - view.scrollTop - view.clientHeight < 40;
    var span = document.createElement("span");
    span.className = "l-" + line.level;
    span.textContent = line.text;
    view.appendChild(span);
    view.appendChild(document.createTextNode("\n"));
    while (view.childNodes.length > max * 2) {
      view.removeChild(view.firstChild);
    }
    count.textContent = "";
    if (atBottom) {
      view.scrollTop = view.scrollHeight;
    }
  };
  src.onerror = function () {
    if (src.readyState === EventSource.CLOSED) {
      state.textContent = "";
    }
  };
});
