const fs = require('fs');
const path = require('path');

const projectRoot = 'c:/Users/admin/Downloads/Seta Im Intern/seta-im-intern';
const intermediateDir = path.join(projectRoot, '.understand-anything/intermediate');

const batches = [4, 5, 6, 7];

for (const batchIndex of batches) {
    const resultsFile = path.join(projectRoot, '.understand-anything/tmp/ua-file-extract-results-' + batchIndex + '.json');
    if (!fs.existsSync(resultsFile)) {
        console.error('Missing results file for batch ' + batchIndex);
        continue;
    }
    const data = JSON.parse(fs.readFileSync(resultsFile, 'utf8'));

    // Also read the original input to get batchImportData and neighborMap
    const inputFile = path.join(projectRoot, '.understand-anything/tmp/ua-file-analyzer-input-' + batchIndex + '.json');
    const inputData = JSON.parse(fs.readFileSync(inputFile, 'utf8'));
    const importData = inputData.batchImportData || {};

    const nodes = [];
    const edges = [];

    for (const fileResult of data.results) {
        const filePath = fileResult.path;
        const category = fileResult.fileCategory;

        let type = 'file';
        if (category === 'config') type = 'config';
        if (category === 'docs') type = 'document';
        if (category === 'infra') {
            if (filePath.match(/Dockerfile|docker-compose/)) type = 'service';
            else if (filePath.match(/\.github\/workflows|\.gitlab-ci|Jenkinsfile/)) type = 'pipeline';
            else type = 'resource';
        }
        if (category === 'data') {
            if (filePath.endsWith('.sql')) type = 'table';
            else if (filePath.match(/\.(graphql|proto|prisma)$/)) type = 'schema';
            else type = 'endpoint';
        }

        let tags = ['file'];
        let summary = 'A file in the project.';

        if (category === 'code') {
            if (filePath.match(/test/i)) {
                tags = ['test'];
                summary = 'Test file.';
            } else if (filePath.match(/middleware/)) {
                tags = ['middleware'];
                summary = 'Middleware logic.';
            } else if (filePath.match(/handler/)) {
                tags = ['api-handler'];
                summary = 'API handler for specific endpoints.';
            } else if (filePath.match(/context/)) {
                tags = ['utility'];
                summary = 'Request context handling.';
            } else {
                tags = ['component'];
            }
        } else if (category === 'data') {
            tags = ['database', 'migration'];
            summary = 'Database schema migration script.';
        } else if (category === 'config') {
            tags = ['configuration'];
            summary = 'Configuration file.';
        } else if (category === 'docs') {
            tags = ['documentation'];
            summary = 'Documentation file.';
        }

        let complexity = 'simple';
        const lines = fileResult.nonEmptyLines || 0;
        if (lines > 200) complexity = 'complex';
        else if (lines > 50) complexity = 'moderate';

        nodes.push({
            id: type + ':' + filePath,
            type: type,
            name: path.basename(filePath),
            filePath: filePath,
            summary: summary,
            tags: tags,
            complexity: complexity
        });

        // Add import edges for code files
        if (category === 'code' && importData[filePath]) {
            for (const targetPath of importData[filePath]) {
                edges.push({
                    source: type + ':' + filePath,
                    target: 'file:' + targetPath,
                    type: 'imports',
                    direction: 'forward',
                    weight: 0.7
                });
            }
        }

        // Functions and classes for code files
        if (category === 'code') {
            if (fileResult.functions) {
                for (const func of fileResult.functions) {
                    if ((func.endLine - func.startLine >= 10) || func.isExported) {
                        const funcId = 'function:' + filePath + ':' + func.name;
                        nodes.push({
                            id: funcId,
                            type: 'function',
                            name: func.name,
                            summary: 'Function ' + func.name + '.',
                            tags: ['utility'],
                            complexity: (func.endLine - func.startLine > 50) ? 'moderate' : 'simple',
                            lineRange: [func.startLine, func.endLine]
                        });
                        edges.push({
                            source: type + ':' + filePath,
                            target: funcId,
                            type: 'contains',
                            direction: 'forward',
                            weight: 1.0
                        });
                        if (func.isExported) {
                            edges.push({
                                source: type + ':' + filePath,
                                target: funcId,
                                type: 'exports',
                                direction: 'forward',
                                weight: 0.8
                            });
                        }
                    }
                }
            }
            if (fileResult.classes) {
                for (const cls of fileResult.classes) {
                    if ((cls.endLine - cls.startLine >= 20) || (cls.methods && cls.methods.length >= 2) || cls.isExported) {
                        const clsId = 'class:' + filePath + ':' + cls.name;
                        nodes.push({
                            id: clsId,
                            type: 'class',
                            name: cls.name,
                            summary: 'Class ' + cls.name + '.',
                            tags: ['data-model'],
                            complexity: 'moderate',
                            lineRange: [cls.startLine, cls.endLine]
                        });
                        edges.push({
                            source: type + ':' + filePath,
                            target: clsId,
                            type: 'contains',
                            direction: 'forward',
                            weight: 1.0
                        });
                        if (cls.isExported) {
                            edges.push({
                                source: type + ':' + filePath,
                                target: clsId,
                                type: 'exports',
                                direction: 'forward',
                                weight: 0.8
                            });
                        }
                    }
                }
            }
        }

        // Additional sub-nodes for data/config/etc
        if (fileResult.definitions) {
            for (const def of fileResult.definitions) {
                 // For SQL, we might extract tables, but let's assume they are handled by script
                 if (def.kind === 'table') {
                     const tableId = 'table:' + filePath + ':' + def.name;
                     nodes.push({
                         id: tableId,
                         type: 'table',
                         name: def.name,
                         summary: 'Table ' + def.name + ' definition.',
                         tags: ['database', 'data-model'],
                         complexity: 'simple'
                     });
                     edges.push({
                         source: type + ':' + filePath,
                         target: tableId,
                         type: 'contains',
                         direction: 'forward',
                         weight: 1.0
                     });
                     // migrates edge from file to table
                     edges.push({
                         source: type + ':' + filePath,
                         target: tableId,
                         type: 'migrates',
                         direction: 'forward',
                         weight: 0.7
                     });
                 }
            }
        }

        // For docs and config, edge rules
        if (category === 'config' && filePath.match(/config\.json/)) {
             edges.push({
                 source: type + ':' + filePath,
                 target: 'file:services/access-core/src/index.ts',
                 type: 'configures',
                 direction: 'forward',
                 weight: 0.6
             });
        }
    }

    // Write out the result using split logic
    const nodeCount = nodes.length;
    const edgeCount = edges.length;

    if (nodeCount <= 60 && edgeCount <= 120) {
        fs.writeFileSync(path.join(intermediateDir, 'batch-' + batchIndex + '.json'), JSON.stringify({nodes, edges}, null, 2));
        console.log('Wrote batch-' + batchIndex + '.json');
    } else {
        const parts = Math.ceil(Math.max(nodeCount / 60, edgeCount / 120));
        // Sort files
        const files = Array.from(new Set(nodes.filter(n => n.filePath).map(n => n.filePath))).sort();
        const filesPerPart = Math.ceil(files.length / parts);

        for (let i=0; i<parts; i++) {
            const partFiles = new Set(files.slice(i*filesPerPart, (i+1)*filesPerPart));
            const partNodes = nodes.filter(n => partFiles.has(n.filePath));
            const partNodeIds = new Set(partNodes.map(n => n.id));
            const partEdges = edges.filter(e => partNodeIds.has(e.source));

            fs.writeFileSync(path.join(intermediateDir, 'batch-' + batchIndex + '-part-' + (i+1) + '.json'), JSON.stringify({nodes: partNodes, edges: partEdges}, null, 2));
            console.log('Wrote batch-' + batchIndex + '-part-' + (i+1) + '.json');
        }
    }
}
