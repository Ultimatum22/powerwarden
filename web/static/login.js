// Drives the passkey login ceremony on the login page. Uses the modern
// PublicKeyCredential JSON helpers (widely supported) instead of hand
// rolling base64url<->ArrayBuffer conversions.
(function () {
  "use strict";

  function showError(msg) {
    document.getElementById("login-error").textContent = msg;
  }

  async function postJSON(url, headers, body) {
    const resp = await fetch(url, {
      method: "POST",
      headers: Object.assign({ "Content-Type": "application/json" }, headers || {}),
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!resp.ok) {
      throw new Error(await resp.text());
    }
    return resp.json();
  }

  async function passkeyLogin() {
    showError("");
    try {
      const begin = await postJSON("/login/passkey/begin");
      const options = PublicKeyCredential.parseRequestOptionsFromJSON(begin.publicKey);
      const credential = await navigator.credentials.get({ publicKey: options });
      const finish = await postJSON(
        "/login/passkey/finish",
        { "X-Challenge-Id": begin.challengeId },
        credential.toJSON()
      );
      window.location.href = finish.redirect || "/";
    } catch (err) {
      showError("Passkey sign-in failed. " + (err && err.message ? err.message : ""));
    }
  }

  document.getElementById("passkey-btn").addEventListener("click", passkeyLogin);

  document.getElementById("totp-link").addEventListener("click", function (e) {
    e.preventDefault();
    document.getElementById("totp-form").style.display = "block";
    document.getElementById("passkey-btn").style.display = "none";
    e.target.style.display = "none";
  });

  document.getElementById("totp-form").addEventListener("submit", async function (e) {
    e.preventDefault();
    showError("");
    const code = document.getElementById("code").value;
    try {
      const resp = await fetch("/login/totp", {
        method: "POST",
        headers: { "Content-Type": "application/x-www-form-urlencoded" },
        body: "code=" + encodeURIComponent(code),
      });
      if (!resp.ok) {
        throw new Error(await resp.text());
      }
      const data = await resp.json();
      window.location.href = data.redirect || "/";
    } catch (err) {
      showError("Invalid code. " + (err && err.message ? err.message : ""));
    }
  });

  // Auto-start the passkey ceremony if the browser supports conditional
  // UI (autofill-style passkey prompt); otherwise the button above is the
  // primary path.
  if (window.PublicKeyCredential && PublicKeyCredential.isConditionalMediationAvailable) {
    PublicKeyCredential.isConditionalMediationAvailable().catch(function () {});
  }
})();
