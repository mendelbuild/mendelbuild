/**
 * DAG rendering utilities using dagre.js
 * Shared between roadmap.html and decision_roadmap.html
 */

const RoadmapDAG = {
    // Status colors for hop nodes
    colors: {
        pending: { fill: '#fff3cd', stroke: '#ffc107', text: '#856404' },
        active: { fill: '#cce5ff', stroke: '#007bff', text: '#004085' },
        selecting: { fill: '#fff3cd', stroke: '#ffc107', text: '#856404' },
        completed: { fill: '#d4edda', stroke: '#28a745', text: '#155724' },
        rejected: { fill: '#f8d7da', stroke: '#dc3545', text: '#721c24' },
        abandoned: { fill: '#e2e3e5', stroke: '#6c757d', text: '#383d41' },
        new: { fill: '#fff', stroke: '#28a745', text: '#28a745', dashed: true },
        // Variation statuses
        creating: { fill: '#cce5ff', stroke: '#007bff', text: '#004085' },
        error: { fill: '#f8d7da', stroke: '#dc3545', text: '#721c24' },
        // terminated is a code/test failure, not a clean shutdown; it shares the
        // failure palette with error and rejected rather than the inert grey.
        terminated: { fill: '#f8d7da', stroke: '#dc3545', text: '#721c24' },
        merged: { fill: '#d4edda', stroke: '#28a745', text: '#155724' },
    },

    /**
     * Render a simple DAG of hops
     * @param {HTMLElement} container - Container element for the SVG
     * @param {Array} nodes - Array of {id, name, status, isTerminal, isNew}
     * @param {Array} edges - Array of {from, to}
     * @param {Object} options - Optional config {nodeWidth, nodeHeight, direction}
     */
    renderSimple: function(container, nodes, edges, options) {
        options = options || {};
        const NODE_WIDTH = options.nodeWidth || 160;
        const NODE_HEIGHT = options.nodeHeight || 40;
        const DIRECTION = options.direction || 'LR';

        if (!container || nodes.length === 0) {
            container.innerHTML = '<p style="text-align: center; color: #666; padding: 20px;">No hops to display.</p>';
            return;
        }

        const g = new dagre.graphlib.Graph();
        g.setGraph({ rankdir: DIRECTION, nodesep: 30, ranksep: 60, marginx: 20, marginy: 20 });
        g.setDefaultEdgeLabel(() => ({}));

        // Add nodes
        nodes.forEach(node => {
            g.setNode(node.id, { width: NODE_WIDTH, height: NODE_HEIGHT, data: node });
        });

        // Add edges
        edges.forEach(edge => {
            if (g.hasNode(edge.from) && g.hasNode(edge.to)) {
                g.setEdge(edge.from, edge.to);
            }
        });

        dagre.layout(g);

        const graphWidth = g.graph().width + 40;
        const graphHeight = g.graph().height + 40;

        const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
        svg.setAttribute('width', Math.max(graphWidth, container.clientWidth));
        svg.setAttribute('height', Math.max(graphHeight, 120));

        // Arrowhead marker
        const defs = document.createElementNS('http://www.w3.org/2000/svg', 'defs');
        defs.innerHTML = '<marker id="dag-arrowhead" markerWidth="10" markerHeight="7" refX="9" refY="3.5" orient="auto"><polygon points="0 0, 10 3.5, 0 7" fill="#999"/></marker>';
        svg.appendChild(defs);

        // Draw edges
        g.edges().forEach(e => {
            const edge = g.edge(e);
            const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
            let d = 'M ' + edge.points[0].x + ' ' + edge.points[0].y;
            for (let i = 1; i < edge.points.length; i++) {
                d += ' L ' + edge.points[i].x + ' ' + edge.points[i].y;
            }
            path.setAttribute('d', d);
            path.setAttribute('fill', 'none');
            path.setAttribute('stroke', '#999');
            path.setAttribute('stroke-width', '2');
            path.setAttribute('marker-end', 'url(#dag-arrowhead)');
            svg.appendChild(path);
        });

        // Draw nodes
        g.nodes().forEach(nodeId => {
            const node = g.node(nodeId);
            const data = node.data;

            // Determine colors
            let colorKey = data.status || 'pending';
            if (data.isNew) colorKey = 'new';
            if (data.isTerminal) colorKey = data.status || 'completed';
            const colors = this.colors[colorKey] || this.colors.pending;

            // Group for hover behavior
            const group = document.createElementNS('http://www.w3.org/2000/svg', 'g');
            group.style.cursor = 'pointer';

            const rect = document.createElementNS('http://www.w3.org/2000/svg', 'rect');
            rect.setAttribute('x', node.x - NODE_WIDTH / 2);
            rect.setAttribute('y', node.y - NODE_HEIGHT / 2);
            rect.setAttribute('width', NODE_WIDTH);
            rect.setAttribute('height', NODE_HEIGHT);
            rect.setAttribute('rx', 5);
            rect.setAttribute('fill', colors.fill);
            rect.setAttribute('stroke', colors.stroke);
            rect.setAttribute('stroke-width', '2');
            if (colors.dashed) {
                rect.setAttribute('stroke-dasharray', '5,3');
            }
            group.appendChild(rect);

            // Add title for native tooltip on hover
            const title = document.createElementNS('http://www.w3.org/2000/svg', 'title');
            const label = data.name || nodeId;
            title.textContent = label;
            group.appendChild(title);

            const text = document.createElementNS('http://www.w3.org/2000/svg', 'text');
            text.setAttribute('x', node.x);
            text.setAttribute('y', node.y + 5);
            text.setAttribute('text-anchor', 'middle');
            text.setAttribute('font-size', '12');
            text.setAttribute('fill', colors.text);
            text.textContent = label.length > 20 ? label.substring(0, 18) + '...' : label;
            group.appendChild(text);

            svg.appendChild(group);
        });

        container.innerHTML = '';
        container.appendChild(svg);
    },

    /**
     * Render a complex DAG with variations inside each hop node
     * Used by the main roadmap view
     * @param {HTMLElement} container - Container element
     * @param {Array} hops - Array of hop objects with Variations array
     * @param {Array} edges - Array of {from, to}
     * @param {string} projectID - Project ID for links
     * @param {Object} options - Optional config
     */
    renderWithVariations: function(container, hops, edges, projectID, options) {
        options = options || {};
        const NODE_WIDTH = options.nodeWidth || 200;
        const NODE_PADDING = 15;
        const VARIATION_HEIGHT = 24;
        const HEADER_HEIGHT = 40;

        if (!container || hops.length === 0) {
            container.innerHTML = '<p class="empty" style="text-align: center; padding: 50px;">No hops in roadmap yet.</p>';
            return;
        }

        function calculateNodeHeight(hop) {
            const variationCount = hop.Variations ? hop.Variations.length : 0;
            if (hop.Status === 'pending') {
                return HEADER_HEIGHT + VARIATION_HEIGHT + NODE_PADDING * 2;
            }
            return HEADER_HEIGHT + (variationCount * VARIATION_HEIGHT) + NODE_PADDING * 2;
        }

        const g = new dagre.graphlib.Graph();
        g.setGraph({ rankdir: 'LR', nodesep: 50, ranksep: 80, marginx: 20, marginy: 20 });
        g.setDefaultEdgeLabel(() => ({}));

        // Add nodes
        hops.forEach(hop => {
            const height = calculateNodeHeight(hop);
            g.setNode(hop.ID, { width: NODE_WIDTH, height: height, hop: hop });
        });

        // Add edges
        if (edges) {
            edges.forEach(edge => {
                g.setEdge(edge.from, edge.to);
            });
        }

        dagre.layout(g);

        const graphWidth = g.graph().width + 40;
        const graphHeight = g.graph().height + 40;

        const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
        svg.setAttribute('width', Math.max(graphWidth, container.clientWidth));
        svg.setAttribute('height', graphHeight);
        svg.style.display = 'block';

        // Arrowhead marker
        const defs = document.createElementNS('http://www.w3.org/2000/svg', 'defs');
        defs.innerHTML = '<marker id="dag-arrowhead" markerWidth="10" markerHeight="7" refX="9" refY="3.5" orient="auto"><polygon points="0 0, 10 3.5, 0 7" fill="#999"/></marker>';
        svg.insertBefore(defs, svg.firstChild);

        // Draw edges
        g.edges().forEach(e => {
            const edge = g.edge(e);
            const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
            let d = 'M ' + edge.points[0].x + ' ' + edge.points[0].y;
            for (let i = 1; i < edge.points.length; i++) {
                d += ' L ' + edge.points[i].x + ' ' + edge.points[i].y;
            }
            path.setAttribute('d', d);
            path.setAttribute('fill', 'none');
            path.setAttribute('stroke', '#999');
            path.setAttribute('stroke-width', '2');
            path.setAttribute('marker-end', 'url(#dag-arrowhead)');
            svg.appendChild(path);
        });

        const self = this;

        // Draw nodes
        g.nodes().forEach(nodeId => {
            const node = g.node(nodeId);
            const hop = node.hop;
            const colors = self.colors[hop.Status] || self.colors.pending;

            const group = document.createElementNS('http://www.w3.org/2000/svg', 'g');
            group.style.cursor = 'pointer';
            group.addEventListener('click', () => {
                window.location.href = '/p/' + projectID + '/hops/' + hop.ID;
            });

            // Background rect
            const rect = document.createElementNS('http://www.w3.org/2000/svg', 'rect');
            rect.setAttribute('x', node.x - NODE_WIDTH / 2);
            rect.setAttribute('y', node.y - node.height / 2);
            rect.setAttribute('width', NODE_WIDTH);
            rect.setAttribute('height', node.height);
            rect.setAttribute('rx', 8);
            rect.setAttribute('fill', colors.fill);
            rect.setAttribute('stroke', colors.stroke);
            rect.setAttribute('stroke-width', '2');
            group.appendChild(rect);

            // Header
            const headerY = node.y - node.height / 2 + 25;
            const title = document.createElementNS('http://www.w3.org/2000/svg', 'text');
            title.setAttribute('x', node.x);
            title.setAttribute('y', headerY);
            title.setAttribute('text-anchor', 'middle');
            title.setAttribute('font-weight', 'bold');
            title.setAttribute('font-size', '13');
            title.setAttribute('fill', colors.text);
            title.textContent = hop.Name.length > 22 ? hop.Name.substring(0, 20) + '...' : hop.Name;
            group.appendChild(title);

            // Variations or status text
            let variationY = headerY + 25;
            if (hop.Status === 'pending') {
                const pendingText = document.createElementNS('http://www.w3.org/2000/svg', 'text');
                pendingText.setAttribute('x', node.x);
                pendingText.setAttribute('y', variationY);
                pendingText.setAttribute('text-anchor', 'middle');
                pendingText.setAttribute('font-size', '11');
                pendingText.setAttribute('fill', '#666');
                pendingText.textContent = 'Hop pending';
                group.appendChild(pendingText);
            } else if (hop.Variations) {
                hop.Variations.forEach(v => {
                    const vColors = self.colors[v.Status] || self.colors.pending;
                    const vText = document.createElementNS('http://www.w3.org/2000/svg', 'text');
                    vText.setAttribute('x', node.x - NODE_WIDTH / 2 + 15);
                    vText.setAttribute('y', variationY);
                    vText.setAttribute('font-size', '11');
                    vText.setAttribute('fill', vColors.text);
                    vText.textContent = '• ' + (v.Name.length > 20 ? v.Name.substring(0, 18) + '...' : v.Name);
                    group.appendChild(vText);
                    variationY += VARIATION_HEIGHT;
                });
            }

            svg.appendChild(group);
        });

        container.innerHTML = '';
        container.appendChild(svg);
    }
};
