// URLs.
//
// The app was navigated entirely by assigning to signals, which meant Back left
// the app, a reload landed at the library root, and nothing could be linked or
// bookmarked. The Go server has served index.html for every non-/api path since
// the beginning, with a comment saying it does so "so client-side routes
// survive a reload or a shared link" -- this is the other half of that.
//
// # One direction
//
// Every user action calls navigate(); only applyRoute() writes the signals that
// say what is on screen. Nothing watches a signal in order to write history, so
// there is no cycle between the two and no "who wins" to reason about.
//
// The exceptions are deliberate and are not navigation: forgetInViewer closing
// the lightbox because the photo in it was just deleted, and resetLibrary
// wiping everything on logout. Those are the state reacting to itself, and
// routing them would be exactly the feedback loop this avoids.

import { api, type Media } from "./api";
import {
  albumItems,
  currentAlbum,
  openAlbum,
  openLibraryRoot,
} from "./albums";
import { hasMore, items, loadMore, view, type View } from "./state";
import {
  clearSearch,
  loadTrash,
  runSearch,
  searchQuery,
  searchResults,
  trashed,
} from "./trash";
import { closeLightbox, lightbox, lightboxScope, openLightbox } from "./viewer";

/**
 * Where the app is.
 *
 * Discriminated by the same `View` the rest of the app already uses rather than
 * a parallel enum, so applyRoute can assign it across verbatim and there is no
 * translation table to keep in step.
 */
export type Route =
  | { view: "album"; albumID: number | null; photo: number | null }
  | { view: "all"; photo: number | null }
  | { view: "trash"; photo: number | null }
  | { view: "search"; query: string; photo: number | null };

/** The route for the library root, which is also the fallback for anything unparseable. */
export const ROOT: Route = { view: "album", albumID: null, photo: null };

// --- the pure half ----------------------------------------------------------

/**
 * Reads a URL.
 *
 * Anything unrecognised is the root rather than an error: a URL is user input,
 * and a typo should land somewhere sensible instead of on a broken screen.
 */
export function parse(pathname: string, search: string): Route {
  const query = new URLSearchParams(search).get("q") ?? "";

  let m = /^\/a\/(\d+)(?:\/p\/(\d+))?\/?$/.exec(pathname);
  if (m) return { view: "album", albumID: Number(m[1]), photo: photoOf(m[2]) };

  m = /^\/all(?:\/p\/(\d+))?\/?$/.exec(pathname);
  if (m) return { view: "all", photo: photoOf(m[1]) };

  m = /^\/trash(?:\/p\/(\d+))?\/?$/.exec(pathname);
  if (m) return { view: "trash", photo: photoOf(m[1]) };

  // A search with nothing to search for is the root. Typing into the box never
  // produces that state either -- clearing the box leaves search -- so a
  // hand-typed /search should not be the one way to reach "0 results for ''".
  m = /^\/search(?:\/p\/(\d+))?\/?$/.exec(pathname);
  if (m && query.trim()) return { view: "search", query, photo: photoOf(m[1]) };

  return ROOT;
}

/** Writes a URL. The inverse of parse for every route parse can produce. */
export function format(route: Route): string {
  let path: string;
  switch (route.view) {
    case "album":
      path = route.albumID === null ? "/" : `/a/${route.albumID}`;
      break;
    case "all":
      path = "/all";
      break;
    case "trash":
      path = "/trash";
      break;
    case "search":
      path = "/search";
      break;
  }
  if (route.photo !== null) path = `${path === "/" ? "" : path}/p/${route.photo}`;
  if (route.view === "search") path += `?${new URLSearchParams({ q: route.query })}`;
  return path;
}

/** The same route without whatever photo was open on top of it. */
export function withoutPhoto(route: Route): Route {
  return { ...route, photo: null };
}

function photoOf(raw: string | undefined): number | null {
  return raw ? Number(raw) : null;
}

// --- history ----------------------------------------------------------------

/** Marks the entries this app pushed, so it can tell whether Back has anywhere to go. */
interface NavState {
  fromRouter: true;
}

export function currentRoute(): Route {
  return parse(location.pathname, location.search);
}

function pushedByUs(): boolean {
  return (history.state as NavState | null)?.fromRouter === true;
}

export function navigate(route: Route, opts: { replace?: boolean } = {}): void {
  const url = format(route);
  const state: NavState = { fromRouter: true };
  // Replacing when the URL is unchanged keeps Back from having to walk through
  // entries that all look the same.
  if (opts.replace || url === location.pathname + location.search) {
    history.replaceState(state, "", url);
  } else {
    history.pushState(state, "", url);
  }
  void applyRoute(route);
}

/**
 * Closes the photo the way Back would.
 *
 * Opening one pushed an entry, so the honest way to close it is to pop that
 * entry -- otherwise Back afterwards would re-open the photo just closed.
 * Unless there is nothing to pop: someone who opened a link straight to a photo
 * has this app's first entry under them, and history.back() would take them out
 * of the app entirely.
 */
export function closePhoto(): void {
  if (pushedByUs()) history.back();
  else navigate(withoutPhoto(currentRoute()), { replace: true });
}

/**
 * Steps the lightbox to another photo.
 *
 * Replaces rather than pushes: walking a shoot of two hundred frames should
 * leave one entry to go back to, not two hundred.
 */
export function showPhoto(id: number): void {
  navigate({ ...currentRoute(), photo: id }, { replace: true });
}

/**
 * Steps the lightbox one photo forward or back, round and round.
 *
 * Past the last photo comes the first again, so a slideshow never runs into a
 * wall. Except in the timeline while there are pages still to fetch: there
 * "the last photo" is only the last one loaded, so the next page is fetched
 * and stepped into first, and the wrap waits until the library really ends.
 * Going back from the first photo of a part-loaded timeline lands on the last
 * one loaded -- fetching the whole library to find the true end is not worth
 * a single keypress.
 */
export async function stepLightbox(delta: 1 | -1): Promise<void> {
  const open = lightbox.value;
  if (!open) return;
  let scope = lightboxScope.value;
  let index = scope.findIndex((m) => m.id === open.id);
  if (index < 0 || scope.length < 2) return;

  let next = index + delta;
  if (next >= scope.length && currentRoute().view === "all" && hasMore()) {
    await loadMore();
    // Somebody may have closed the photo, or stepped elsewhere, meanwhile.
    if (lightbox.value?.id !== open.id) return;
    scope = items.value;
    index = scope.findIndex((m) => m.id === open.id);
    next = index + 1;
    // Still more to come but nothing arrived: a failed or overlapping fetch.
    // Staying put beats leaping back to the newest photo.
    if (next >= scope.length && hasMore()) return;
  }
  next = (next + scope.length) % scope.length;
  const target = scope[next];
  if (target) showPhoto(target.id);
}

/**
 * Runs a search from the box.
 *
 * Typing replaces, so a query does not leave one history entry per keystroke;
 * entering and leaving search push, so one Back gets out of the results.
 */
export function navigateSearch(query: string): void {
  const from = currentRoute();
  const next: Route = query.trim()
    ? { view: "search", query, photo: null }
    : ROOT;
  navigate(next, { replace: from.view === "search" && next.view === "search" });
}

let started = false;

/**
 * Points the app at whatever is in the address bar, and keeps it there.
 *
 * Called on entering the gallery, which is what makes a deep link survive the
 * login screen: nothing on the way in touches the URL, so the address bar is
 * the pending destination for the whole of the login flow, with nowhere to
 * stash it and nothing to remember.
 */
export function startRouting(): void {
  if (!started) {
    started = true;
    window.addEventListener("popstate", () => void applyRoute(currentRoute()));
  }
  navigate(currentRoute(), { replace: true });
}

// --- applying ---------------------------------------------------------------

/**
 * Which applyRoute call is the current one.
 *
 * openAlbum and the rest write shared signals when their own fetch resolves,
 * which a later navigation cannot call off. So a stale continuation does not
 * merely stop -- it re-dispatches the route that is actually in the address
 * bar, which fetches the right thing over the top of what it clobbered. The
 * search already drops out-of-order responses with a token like this one.
 */
let generation = 0;

/**
 * The view the last applied route was on.
 *
 * Opening a photo, and stepping from one to the next, are routes too -- and
 * they differ from the one before only in which photo is named. Without
 * knowing that the view itself did not change, every arrow press would refetch
 * the collection underneath the lightbox.
 */
let appliedView: View | null = null;

export async function applyRoute(route: Route): Promise<void> {
  const mine = ++generation;
  const current = () => mine === generation;
  const from = appliedView;
  appliedView = route.view;

  view.value = route.view;
  if (route.view !== "search") clearSearch();

  switch (route.view) {
    case "album":
      // The open album says which one it is, so it doubles as the check for
      // whether this route needs a fetch at all.
      if (route.albumID === null) {
        openLibraryRoot();
      } else if (currentAlbum.value?.id !== route.albumID) {
        await openAlbum(route.albumID);
        if (!current()) return void applyRoute(currentRoute());
      }
      break;
    case "all":
      openLibraryRoot();
      break;
    case "trash":
      openLibraryRoot();
      if (from !== "trash") {
        await loadTrash();
        if (!current()) return void applyRoute(currentRoute());
      }
      break;
    case "search":
      openLibraryRoot();
      // clearSearch above emptied the query for any route that is not a
      // search, so arriving from elsewhere always searches afresh.
      if (searchQuery.value !== route.query) {
        await runSearch(route.query);
        if (!current()) return void applyRoute(currentRoute());
      }
      break;
  }

  await applyPhoto(route, current);
}

async function applyPhoto(route: Route, current: () => boolean): Promise<void> {
  if (route.photo === null) {
    closeLightbox();
    return;
  }

  const scope = scopeFor(route);
  const found = scope.find((m) => m.id === route.photo);
  if (found) {
    openLightbox(found, scope);
    return;
  }

  // Not in what is loaded. That covers a photo further down the timeline than
  // anyone has scrolled, and a page still loading, and there is no endpoint
  // that pages to a named id -- so fetch the one photo and show it alone. The
  // lightbox already hides its arrows for a scope of one.
  try {
    const media = await api.getMedia(route.photo);
    if (!current()) return;
    openLightbox(media, [media]);
  } catch {
    // A link to a photo that has since been deleted. Drop it from the URL
    // rather than leaving a dead address in the bar.
    if (current()) navigate(withoutPhoto(route), { replace: true });
  }
}

/** The collection the lightbox's arrows walk, for the view it was opened from. */
function scopeFor(route: Route): Media[] {
  switch (route.view) {
    case "all":
      return items.value;
    case "album":
      return albumItems.value;
    case "trash":
      return trashed.value;
    case "search":
      return searchResults.value;
  }
}
