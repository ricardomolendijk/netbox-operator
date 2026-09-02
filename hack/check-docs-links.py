#!/usr/bin/env python3
"""Check docs/ links, and check what the docs site build published (NBO-086).

Two phases, both run by `make docs-check`:

  source  Every relative link and heading anchor under docs/ resolves the way GitHub
          resolves it. This is the check that was being done by hand on every docs PR.
          Plus: every page under docs/ is reachable from docs/README.md, the hand-
          maintained index. mkdocs.yml's `nav:` is the site's sidebar and `mkdocs build
          --strict` already fails on a page the nav omits, so that half is covered there;
          what this catches is the other half, a page nobody indexed and nobody linked,
          which the nav alone would happily publish as an orphan.

  site    Run against site/ after `mkdocs build`. Asserts:
          * every published file traces back to a path under docs/ -- the allowlist
            assertion. plan.md, roadmap.md, specs/ and any future top-level directory
            fail this by construction rather than by being named in a denylist;
          * no published path is named plan.*, roadmap.* or specs/ anyway, because that
            is the acceptance criterion and it should fail loudly and by name;
          * every internal href in the built HTML resolves, including its fragment, so a
            link that MkDocs rewrote is verified against the HTML it actually produced;
          * every ../ link that hack/mkdocs_hooks.py rewrote to a blob URL points at a
            file that exists in this checkout.

Usage: check-docs-links.py [--site DIR]
"""

import argparse
import os
import posixpath
import re
import sys
import unicodedata

REPO = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DOCS = os.path.join(REPO, "docs")
BLOB_BASE = "https://github.com/ricardomolendijk/netbox-operator/blob/main/"

# Paths a published site may contain that have no docs/ source: MkDocs and the theme
# generate them. Anything else must come from docs/.
THEME_OWNED = ("assets/",)
THEME_FILES = {"404.html", "sitemap.xml", "sitemap.xml.gz", "search/search_index.json"}

# What must never be published, by name, in addition to the provenance check above.
FORBIDDEN_DIRS = {"specs"}
FORBIDDEN_STEMS = {"plan", "roadmap"}

MD_LINK = re.compile(r"\]\(\s*([^)\s]+)")
CODE_SPAN = re.compile(r"`[^`\n]*`")
FENCE = re.compile(r"^\s*(```|~~~)")
# GitHub anchors headings nested in a blockquote too, and docs/ relies on that: the
# "site is a real foreign key" callout at the top of netboxvlan.md is linked by anchor.
HEADING = re.compile(r"^[ >]*(#{1,6})\s+(.*?)\s*#*\s*$")
HREF = re.compile(r"""\shref=["']([^"']+)["']""")
HTML_ID = re.compile(r"""\s(?:id|name)=["']([^"']+)["']""")

errors: list[str] = []


def fail(msg: str) -> None:
    errors.append(msg)


def gh_slug(text: str) -> str:
    """GitHub's heading-anchor slug for a Markdown heading's raw text."""
    text = re.sub(r"`([^`]*)`", r"\1", text)  # code spans
    text = re.sub(r"!?\[([^\]]*)\]\([^)]*\)", r"\1", text)  # links, images
    text = text.replace("**", "").replace("<br>", "")
    text = unicodedata.normalize("NFKD", text)
    text = re.sub(r"[^\w\- ]", "", text.strip().lower(), flags=re.UNICODE)
    return text.replace(" ", "-")


def anchors_of(path: str) -> set[str]:
    """Heading anchors GitHub would generate for a Markdown file, duplicates numbered."""
    out: set[str] = set()
    seen: dict[str, int] = {}
    in_fence = False
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            if FENCE.match(line):
                in_fence = not in_fence
                continue
            if in_fence:
                continue
            m = HEADING.match(line)
            if not m:
                continue
            base = gh_slug(m.group(2))
            n = seen.get(base, 0)
            seen[base] = n + 1
            out.add(base if n == 0 else f"{base}-{n}")
    # Explicit <a name="..."> / <a id="..."> anchors, if any file grows one.
    with open(path, encoding="utf-8") as fh:
        out.update(HTML_ID.findall(fh.read()))
    return out


def md_files(root: str):
    for dirpath, _, names in os.walk(root):
        for name in sorted(names):
            if name.endswith(".md"):
                yield os.path.join(dirpath, name)


def check_source() -> None:
    anchors: dict[str, set[str]] = {}

    def anchors_for(path: str) -> set[str]:
        if path not in anchors:
            anchors[path] = anchors_of(path)
        return anchors[path]

    for md in md_files(DOCS):
        rel = os.path.relpath(md, REPO)
        with open(md, encoding="utf-8") as fh:
            body = fh.read()
        # Code spans and fenced blocks are not links; docs/ is full of regex patterns
        # like `[-a-z0-9]*[a-z0-9]` that look like one.
        body = CODE_SPAN.sub("`code`", re.sub(r"(?ms)^(```|~~~).*?^\1", "", body))
        for target in MD_LINK.findall(body):
            if "://" in target or target.startswith(("mailto:", "tel:")):
                continue
            if target.startswith("/"):
                fail(f"{rel}: absolute link {target!r} does not resolve on GitHub")
                continue
            path, _, frag = target.partition("#")
            abspath = md if not path else os.path.normpath(
                os.path.join(os.path.dirname(md), path)
            )
            if not os.path.exists(abspath):
                fail(f"{rel}: link {target!r} does not exist")
                continue
            if not frag:
                continue
            if not abspath.endswith(".md"):
                fail(f"{rel}: link {target!r} anchors into a non-Markdown file")
                continue
            if frag not in anchors_for(abspath):
                fail(f"{rel}: link {target!r} -- no heading '#{frag}' in that file")


def md_links_in(path: str) -> list[str]:
    """Markdown links from `path` to another Markdown file under docs/."""
    with open(path, encoding="utf-8") as fh:
        body = fh.read()
    body = CODE_SPAN.sub("`code`", re.sub(r"(?ms)^(```|~~~).*?^\1", "", body))
    out = []
    for target in MD_LINK.findall(body):
        if "://" in target or target.startswith(("#", "/", "mailto:", "tel:")):
            continue
        resolved = os.path.normpath(
            os.path.join(os.path.dirname(path), target.partition("#")[0])
        )
        if resolved.endswith(".md") and resolved.startswith(DOCS + os.sep):
            out.append(resolved)
    return out


def check_index() -> None:
    """Every page under docs/ is reachable from docs/README.md."""
    index = os.path.join(DOCS, "README.md")
    seen, queue = {index}, [index]
    while queue:
        for nxt in md_links_in(queue.pop()):
            if nxt not in seen:
                seen.add(nxt)
                queue.append(nxt)
    for orphan in sorted(set(md_files(DOCS)) - seen):
        fail(
            f"{os.path.relpath(orphan, REPO)}: not reachable from docs/README.md -- add "
            "it to the index, or link it from a page that is in the index"
        )


def site_source(rel: str) -> str | None:
    """The docs/ source a published path came from, or None if it has none."""
    for candidate in (
        rel[: -len("index.html")] + "index.md" if rel.endswith("index.html") else None,
        rel[: -len("index.html")] + "README.md" if rel.endswith("index.html") else None,
        rel[: -len("/index.html")] + ".md" if rel.endswith("/index.html") else None,
        rel,
    ):
        if candidate and os.path.exists(os.path.join(DOCS, candidate)):
            return candidate
    return None


def check_site(site: str) -> None:
    if not os.path.isdir(site):
        fail(f"{site}: no build output -- run `make docs-build` first")
        return

    published = []
    for dirpath, _, names in os.walk(site):
        for name in names:
            published.append(
                os.path.relpath(os.path.join(dirpath, name), site).replace(os.sep, "/")
            )
    if not published:
        fail(f"{site}: build output is empty")
        return

    for rel in sorted(published):
        parts = rel.split("/")
        if FORBIDDEN_DIRS.intersection(parts):
            fail(f"LEAK: {rel} is published from a directory that must never ship")
        if parts[-1].split(".")[0] in FORBIDDEN_STEMS:
            fail(f"LEAK: {rel} is published and must never ship")
        if rel.startswith(THEME_OWNED) or rel in THEME_FILES:
            continue
        if site_source(rel) is None:
            fail(f"LEAK: {rel} is published but has no source under docs/")

    files = set(published)
    for rel in sorted(f for f in published if f.endswith(".html")):
        with open(os.path.join(site, rel), encoding="utf-8") as fh:
            html = fh.read()
        for href in HREF.findall(html):
            if href.startswith(BLOB_BASE):
                escaped = href[len(BLOB_BASE):].partition("#")[0]
                if not os.path.exists(os.path.join(REPO, escaped)):
                    fail(f"{rel}: rewritten link to {escaped!r} does not exist in repo")
                continue
            if "://" in href or href.startswith(("mailto:", "tel:", "data:", "javascript:")):
                continue
            path, _, frag = href.partition("#")
            if not path:
                target = rel  # same page
            else:
                # 404.html links from the site root, since it is served at any depth.
                base = "" if path.startswith("/") else posixpath.dirname(rel)
                target = posixpath.normpath(posixpath.join(base, path.lstrip("/")))
                if target == ".":
                    target = "index.html"
                if target not in files and posixpath.join(target, "index.html") in files:
                    target = posixpath.join(target, "index.html")
            if target not in files:
                fail(f"{rel}: href {href!r} resolves to {target!r}, which is not built")
                continue
            if frag and target.endswith(".html"):
                with open(os.path.join(site, target), encoding="utf-8") as fh:
                    if frag not in set(HTML_ID.findall(fh.read())):
                        fail(f"{rel}: href {href!r} -- no id '{frag}' in {target}")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--site", help="also check built output in this directory")
    args = ap.parse_args()

    check_source()
    check_index()
    if args.site:
        check_site(args.site)

    if errors:
        for e in errors:
            print(e, file=sys.stderr)
        print(f"\n{len(errors)} issue(s).", file=sys.stderr)
        return 1
    print("0 issues.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
