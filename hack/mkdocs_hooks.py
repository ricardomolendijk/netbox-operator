"""MkDocs build hooks for the docs site (NBO-086).

Two jobs, both of which exist so that `docs/` needs no site-specific front matter or
shortcodes and stays plain Markdown that renders correctly in GitHub's file browser.

1. Relative links that escape `docs/` -- `../../internal/registry/registry.go`,
   `../README.md` -- resolve on GitHub and cannot resolve in a site rooted at `docs/`.
   They are rewritten to `blob/main` URLs on the canonical repository. Rewriting them
   here rather than in the Markdown keeps the in-repo links relative, which is what makes
   them work on a fork and in a local editor.

2. `docs/netbox-schema.md` is 3.6k lines (264 KB) of generated schema reference. It is one
   page of 53 and 21.5% of the indexed text, so it drowns the 40 hand-written pages on
   almost any query -- and the page itself says "grep it; do not read it". Excluding it
   drops the search index from 1,349,852 to 1,074,450 bytes. It stays in the site and in
   the navigation with its full 182-heading TOC; only search skips it.
"""

import posixpath
import re

BLOB_BASE = "https://github.com/ricardomolendijk/netbox-operator/blob/main/"

# Excluded from the search index only -- still built, still navigable, still linkable.
SEARCH_EXCLUDE = {"netbox-schema.md"}

# Inline links and reference definitions. Bare autolinks (<...>) are never relative.
_LINK = re.compile(r"(\]\(\s*)([^)\s]+)")


def _rewrite(src_uri: str, target: str) -> str:
    """Return target, rewritten to an absolute repo URL if it escapes docs/."""
    if "://" in target or target.startswith(("#", "/", "mailto:")):
        return target
    path, _, frag = target.partition("#")
    if not path:
        return target
    resolved = posixpath.normpath(posixpath.join(posixpath.dirname(src_uri), path))
    if not resolved.startswith("../"):
        return target  # still inside docs/; let MkDocs resolve and validate it
    # One ".." lands in the repository root, which is docs/'s parent.
    escaped = resolved[len("../"):]
    if escaped.startswith("../"):
        raise ValueError(f"{src_uri}: link {target!r} points outside the repository")
    return BLOB_BASE + escaped + (f"#{frag}" if frag else "")


def on_page_markdown(markdown, page, config, files):
    if page.file.src_uri in SEARCH_EXCLUDE:
        page.meta["search"] = {"exclude": True}
    return _LINK.sub(
        lambda m: m.group(1) + _rewrite(page.file.src_uri, m.group(2)), markdown
    )
