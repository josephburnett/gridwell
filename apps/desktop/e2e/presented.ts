// The node both presented specs boot: the pages plugin presents
// http://example.test/* (its pages at <host>/<key>, site.Present), a route
// sends those addresses to it, and another sends http://outside.test/* to the
// default browser.
export const PAGES = 'pages01';

export const presentedYaml =
  `plugins:\n    - id: ${PAGES}\n      kind: pages\n      label: site\n      config:\n` +
  `        presents: "http://example.test/*"\n` +
  `routes:\n    - match: "http://example.test/*"\n      plugin: ${PAGES}\n` +
  `    - match: "http://outside.test/*"\n      outside: true\n`;
