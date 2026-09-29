// Page behaviour shared by every signed-in page (layout.html):
//
//  - Step-up: an htmx request from an element marked data-stepup (host
//    shutdown, vacation, ignore-weather) first runs a fresh passkey
//    assertion via /stepup/begin|finish, and is only sent once that
//    succeeds (CLAUDE.md: "Step-up ... need a fresh passkey assertion").
//  - Feedback: failed htmx requests show a generic message in #flash
//    instead of failing silently; details stay in the server log.
(function () {
  "use strict";

  function csrfHeaders() {
    try {
      return JSON.parse(document.body.getAttribute("hx-headers") || "{}");
    } catch (e) {
      return {};
    }
  }

  function flash(msg) {
    const el = document.getElementById("flash");
    if (!el) return;
    el.textContent = msg;
    el.hidden = !msg;
    if (msg) el.scrollIntoView({ block: "nearest" });
  }

  async function postJSON(url, headers, body) {
    const resp = await fetch(url, {
      method: "POST",
      credentials: "same-origin",
      headers: Object.assign({ "Content-Type": "application/json" }, csrfHeaders(), headers || {}),
      body: body ? JSON.stringify(body) : undefined,
    });
    if (resp.status === 401) {
      window.location.href = "/login";
      throw new Error("signed out");
    }
    if (!resp.ok) {
      const err = new Error("step-up failed");
      err.status = resp.status;
      throw err;
    }
    return resp.json();
  }

  async function stepUp() {
    if (!window.PublicKeyCredential || !PublicKeyCredential.parseRequestOptionsFromJSON) {
      throw new Error("This browser does not support passkeys.");
    }
    const begin = await postJSON("/stepup/begin");
    const options = PublicKeyCredential.parseRequestOptionsFromJSON(begin.publicKey);
    const credential = await navigator.credentials.get({ publicKey: options });
    await postJSON("/stepup/finish", { "X-Challenge-Id": begin.challengeId }, credential.toJSON());
  }

  function stepUpMessage(err) {
    if (err && err.name === "NotAllowedError") return "Passkey confirmation was cancelled.";
    if (err && err.status === 429) return "Too many attempts. Wait a moment and try again.";
    if (err && err.message && err.message.indexOf("browser") !== -1) return err.message;
    return "Passkey confirmation failed. Nothing was changed.";
  }

  document.addEventListener("htmx:confirm", function (evt) {
    const elt = evt.detail.elt;
    if (!elt || !elt.closest("[data-stepup]")) return;
    evt.preventDefault();

    // Validate the form before asking for a passkey, so a missing date
    // doesn't cost a confirmation.
    const form = elt.closest("form");
    if (form && !form.reportValidity()) return;

    const busy = form || elt;
    if (busy.getAttribute("aria-busy") === "true") return;
    busy.setAttribute("aria-busy", "true");
    flash("");
    stepUp()
      .then(function () {
        evt.detail.issueRequest(true);
      })
      .catch(function (err) {
        flash(stepUpMessage(err));
      })
      .finally(function () {
        busy.removeAttribute("aria-busy");
      });
  });

  document.addEventListener("htmx:beforeRequest", function () {
    flash("");
  });

  document.addEventListener("htmx:responseError", function (evt) {
    const status = evt.detail.xhr ? evt.detail.xhr.status : 0;
    if (status === 401) {
      window.location.href = "/login";
      return;
    }
    if (status === 403) {
      flash("Not allowed. Reload the page and try again; this action may need passkey confirmation.");
    } else if (status === 429) {
      flash("Too many attempts. Wait a moment and try again.");
    } else if (status >= 400 && status < 500) {
      flash("That request was not accepted. Check the form and try again.");
    } else {
      flash("Something went wrong. Nothing may have changed; see the event log.");
    }
  });

  document.addEventListener("htmx:sendError", function () {
    flash("Can't reach labpower. Check your connection.");
  });
})();
