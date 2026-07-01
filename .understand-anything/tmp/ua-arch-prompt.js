const fs = require('fs');
const path = require('path');
const pluginDir = 'C:\\\\Users\\\\admin\\\\.understand-anything\\\\repo\\\\understand-anything-plugin\\\\skills\\\\understand';
const graph = JSON.parse(fs.readFileSync('.understand-anything/intermediate/assembled-graph.json', 'utf8'));

const langs = ['go', 'json', 'markdown', 'mod', 'prisma', 'sql', 'sum', 'txt', 'typescript', 'unknown', 'yaml'];
const frameworks = ['docker-compose', 'fastify', 'gorm', 'graphql-yoga', 'prisma'];

let injectedContext = '';
for (const lang of langs) {
  const p = path.join(pluginDir, 'languages', lang + '.md');
  if (fs.existsSync(p)) {
    injectedContext += '\n## Language Context: ' + lang + '\n' + fs.readFileSync(p, 'utf8') + '\n';
  }
}
for (const fw of frameworks) {
  const p = path.join(pluginDir, 'frameworks', fw + '.md');
  if (fs.existsSync(p)) {
    injectedContext += '\n## Framework Addendum: ' + fw + '\n' + fs.readFileSync(p, 'utf8') + '\n';
  }
}

const fileLevelTypes = new Set(['file', 'config', 'document', 'service', 'pipeline', 'table', 'schema', 'resource', 'endpoint']);
const fileNodes = graph.nodes.filter(n => fileLevelTypes.has(n.type)).map(n => ({
  id: n.id, type: n.type, name: n.name, filePath: n.filePath, summary: n.summary, tags: n.tags
}));
const importEdges = graph.edges.filter(e => e.type === 'imports');

let prompt = `Analyze this codebase's structure to identify architectural layers.
Project root: c:\\Users\\admin\\Downloads\\Seta Im Intern\\seta-im-intern
Write output to: c:\\Users\\admin\\Downloads\\Seta Im Intern\\seta-im-intern\\.understand-anything\\intermediate\\layers.json
Project: seta-im-intern

**Additional context from main session:**
Frameworks detected: Docker Compose, Fastify, GORM, GraphQL Yoga, Prisma

Directory tree (top 2 levels):
.env.example
.gitignore
.understand-anything\\.understandignore
.understand-anything\\baseline-context.md
.understand-anything\\config.json
.understand-anything\\diff-overlay.json
.understand-anything\\fingerprints.json
.understand-anything\\knowledge-graph.json
.understand-anything\\meta.json
.understand-anything\\understand-tour.md
go-asset-core\\main.exe
infra\\docker-compose.yml
LICENSE
package-lock.json
package.json
pr_body.txt
README.md
scripts\\generate_subagents.js
scripts\\start-ua.js

Use the directory tree, language context, and framework addendums (appended above) to inform layer assignments. Directory structure is strong evidence for layer boundaries. Non-code files (config, docs, infrastructure, data) should be assigned to appropriate layers — see the prompt template for guidance.

${injectedContext}

File nodes (all node types):
${JSON.stringify(fileNodes, null, 2)}

Import edges:
${JSON.stringify(importEdges, null, 2)}

All edges (for cross-category analysis):
${JSON.stringify(graph.edges, null, 2)}`;

fs.writeFileSync('.understand-anything/tmp/ua-arch-prompt.txt', prompt);
console.log('Prompt written. Size: ' + prompt.length);
