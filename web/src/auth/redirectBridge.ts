// msal-browser 5 completes silent (hidden iframe) and popup flows by loading
// the redirect URI in a child window, which must hand the response back to the
// main window with broadcastResponseToMainFrame(). Our redirect URI is the app
// itself, so main.tsx checks this before rendering.

const RESPONSE_PARAM = /[#?&](code|error)=/;
const STATE_PARAM = /[#?&]state=/;

// isAuthResponseInChildWindow reports whether this page load is an Entra
// response delivered to a hidden iframe or popup, rather than a normal visit
// or a top-level redirect sign-in (which handleRedirectPromise processes).
export function isAuthResponseInChildWindow(w: Window): boolean {
  const isChild = w.parent !== w || Boolean(w.opener);
  if (!isChild) return false;
  return [w.location.hash, w.location.search].some(
    (part) => RESPONSE_PARAM.test(part) && STATE_PARAM.test(part),
  );
}
