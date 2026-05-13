/**
 * @fileoverview no-raw-page-shell — screens must wrap content in <AdminPage>.
 *
 * Rationale: docs/12-admin-ui.md §Layout fixes a canonical page contract
 * via the AdminPage compound (header / toolbar / body / empty / error /
 * footer). Without enforcement, every new screen reinvents its outer
 * spacing + header markup, and the admin UI drifts.
 *
 * What this rule does:
 *   - For files matching `admin/src/screens/**.tsx`, inspect the
 *     exported screen component's return value.
 *   - If the top-level JSX element is not <AdminPage> or a whitelisted
 *     base (LoginShell / BootstrapShell — these are pre-auth screens
 *     with different chrome requirements), report.
 *
 * Heuristic: we look at the JSXElement returned by the exported function
 * (or arrow-fn) whose name ends with "Screen". The check is intentionally
 * shallow — it doesn't trace through HOCs or wrapper components. False
 * positives can be silenced with a per-file `eslint-disable-next-line`
 * pragma; do that sparingly and only for shells that genuinely don't fit
 * the contract (e.g. full-screen wizards).
 *
 * Not handled:
 *   - Fragments at the top — flagged as raw shell because <></> bypasses
 *     the contract. Wrap in <AdminPage>.
 *   - Conditional returns — only the first JSX return is checked; if a
 *     screen has multiple branches returning different shells, only the
 *     first is enforced.
 */

const WHITELIST = new Set([
  "AdminPage",
  "LoginShell",
  "BootstrapShell",
]);

/** @type {import('eslint').Rule.RuleModule} */
export default {
  meta: {
    type: "suggestion",
    docs: {
      description:
        "Screens under admin/src/screens/ must use <AdminPage> (or a whitelisted shell) as their top-level JSX element.",
    },
    schema: [],
    messages: {
      rawShell:
        "Screen `{{name}}` returns a raw `<{{tag}}>` shell. Wrap content in <AdminPage> to match the docs/12 §Layout contract.",
      fragmentShell:
        "Screen `{{name}}` returns a Fragment at the top level. Wrap content in <AdminPage>.",
    },
  },

  create(context) {
    const filename = context.filename || context.getFilename();
    // Only enforce on screens/*.tsx files. The rule is intentionally
    // file-scoped — generic components and layout helpers don't need
    // the AdminPage wrapper.
    if (!/[/\\]src[/\\]screens[/\\][^/\\]+\.tsx$/.test(filename)) {
      return {};
    }

    /** Returns the JSX element wrapped by the function, or null. */
    function topJSX(node) {
      if (!node) return null;
      // ArrowFunctionExpression with implicit return: () => <Foo/>
      if (node.type === "ArrowFunctionExpression" && node.body) {
        if (node.body.type === "JSXElement") return node.body;
        if (node.body.type === "JSXFragment") return node.body;
        if (node.body.type === "BlockStatement") {
          return topJSXFromBlock(node.body);
        }
      }
      if (node.type === "FunctionDeclaration" || node.type === "FunctionExpression") {
        return topJSXFromBlock(node.body);
      }
      return null;
    }

    function topJSXFromBlock(block) {
      if (!block || block.type !== "BlockStatement") return null;
      // Walk statements looking for the LAST `return <jsx>` — this
      // matches the common pattern where early-returns handle loading
      // / error and the canonical return is at the bottom.
      let last = null;
      for (const s of block.body) {
        if (s.type === "ReturnStatement" && s.argument) {
          if (s.argument.type === "JSXElement" || s.argument.type === "JSXFragment") {
            last = s.argument;
          }
        }
      }
      return last;
    }

    function checkScreen(node, name) {
      const top = topJSX(node);
      if (!top) return;
      if (top.type === "JSXFragment") {
        context.report({
          node: top,
          messageId: "fragmentShell",
          data: { name },
        });
        return;
      }
      const opening = top.openingElement;
      if (!opening) return;
      const tag = jsxName(opening.name);
      if (!tag) return;
      if (WHITELIST.has(tag)) return;
      // AdminPage.Header / AdminPage.Body would have tag = "AdminPage"
      // but their root is the member expression — `jsxName` returns the
      // object portion ("AdminPage"), so they pass.
      context.report({
        node: top,
        messageId: "rawShell",
        data: { name, tag },
      });
    }

    function jsxName(n) {
      if (!n) return null;
      if (n.type === "JSXIdentifier") return n.name;
      if (n.type === "JSXMemberExpression") return jsxName(n.object);
      return null;
    }

    return {
      ExportNamedDeclaration(node) {
        const decl = node.declaration;
        if (!decl) return;
        if (decl.type === "FunctionDeclaration" && decl.id) {
          const name = decl.id.name;
          if (name.endsWith("Screen") || name.endsWith("Page")) {
            checkScreen(decl, name);
          }
          return;
        }
        if (decl.type === "VariableDeclaration") {
          for (const d of decl.declarations) {
            if (
              d.id &&
              d.id.type === "Identifier" &&
              (d.id.name.endsWith("Screen") || d.id.name.endsWith("Page")) &&
              d.init
            ) {
              checkScreen(d.init, d.id.name);
            }
          }
        }
      },
    };
  },
};
