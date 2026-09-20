// Drives the first-run passkey registration ceremony.
(function () {
  "use strict";

  function showError(msg) {
    document.getElementById("enrol-error").textContent = msg;
  }

  async function post(url, headers, body) {
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

  document.getElementById("enrol-btn").addEventListener("click", async function (e) {
    showError("");
    const token = e.target.getAttribute("data-token");
    try {
      const begin = await post("/enrol/passkey/begin", { "X-Enrol-Token": token });
      const options = PublicKeyCredential.parseCreationOptionsFromJSON(begin.publicKey);
      const credential = await navigator.credentials.create({ publicKey: options });
      const finish = await post(
        "/enrol/passkey/finish",
        { "X-Enrol-Token": token, "X-Challenge-Id": begin.challengeId },
        credential.toJSON()
      );
      window.location.href = finish.redirect || "/";
    } catch (err) {
      showError("Passkey registration failed. " + (err && err.message ? err.message : ""));
    }
  });
})();
