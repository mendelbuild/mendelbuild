/**
 * RoadmapView — the project roadmap DAG, rendered from hop + edge data.
 *
 * This is the one renderer for the roadmap. The full-page view and the small
 * embedded panel on Hop and Variation pages call it with the same data and
 * differ only in options, so the panel is literally the roadmap rather than a
 * second, drifting drawing of it.
 *
 * Requires dagre to be loaded.
 */

const RoadmapView = (function() {

    // Hop and Variation status palettes. Kept in step with domain.Tone; see
    // layout.html for the same six tones in CSS.
    const FILL = {
        pending: '#fff3cd',
        active: '#cce5ff',
        selecting: '#fff3cd',
        completed: '#d4edda',
        rejected: '#f8d7da',
        abandoned: '#e2e3e5',
        proposed: '#e2e3e5',
        creating: '#cce5ff',
        error: '#f8d7da',
        // terminated is a code/test failure, not a clean shutdown.
        terminated: '#f8d7da',
        merged: '#d4edda',
    };

    const INK = {
        pending: '#856404',
        active: '#004085',
        selecting: '#856404',
        completed: '#155724',
        rejected: '#721c24',
        abandoned: '#383d41',
        proposed: '#383d41',
        creating: '#004085',
        error: '#721c24',
        terminated: '#721c24',
        merged: '#155724',
    };

    // Greys for the embedded panel, which dims everything except the Hop and
    // Variation being viewed. The full roadmap page is where the status
    // palette earns its keep; on a panel the reader is looking for one thing,
    // and six competing tones make that thing harder to find, not easier.
    const DIM = {
        fill: '#f8f9fa',
        border: '#e0e2e5',
        ink: '#9aa0a6',
        variationFill: '#eceef0',
        variationInk: '#8b9096',
    };

    // The "you are here" accent, shared by the focused Hop's ring and the
    // focused Variation's fill so the two read as one selection.
    const FOCUS_ACCENT = '#0D4D2D';

    const NODE_WIDTH = 200;
    const NODE_PADDING = 15;
    const VARIATION_HEIGHT = 24;
    const HEADER_HEIGHT = 40;

    // Vertical offset of the first Variation row inside a Hop node, and the
    // step between rows. Used to scroll to a Variation rather than to the
    // middle of a Hop that is taller than the panel.
    const VARIATION_TOP = HEADER_HEIGHT + 5;

    // Below this the labels stop being readable, so a Hop taller than the
    // panel is centred on the Variation of interest instead of shrunk further.
    const MIN_FIT_SCALE = 0.5;

    function nodeHeight(hop) {
        const count = hop.Variations ? hop.Variations.length : 0;
        if (hop.Status === 'pending') {
            // Pending hops show "Hop pending" instead of variations.
            return HEADER_HEIGHT + VARIATION_HEIGHT + NODE_PADDING * 2;
        }
        return HEADER_HEIGHT + (count * VARIATION_HEIGHT) + NODE_PADDING * 2;
    }

    function escapeHTML(s) {
        return String(s == null ? '' : s)
            .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
    }

    /**
     * @param {Object} view
     *   projectID         {string}
     *   focused           {bool}    this is the Hop being viewed
     *   dim               {bool}    grey everything that is not focused
     *   focusVariationID  {string?} the Variation being viewed, if any
     */
    function hopNodeHTML(hop, height, view) {
        const projectID = view.projectID;
        const focused = view.focused;

        // Dimmed Hops lose their status colour entirely. The focused Hop keeps
        // it, which is what makes it the only coloured card on the panel.
        const dimmed = view.dim && !focused;
        const bg = dimmed ? DIM.fill : (FILL[hop.Status] || '#fff');
        const name = escapeHTML(hop.Name);

        // The focused hop has to be findable at a glance in a panel showing a
        // dozen nodes, so it gets a ring rather than a subtly different border.
        const border = focused
            ? `border: 2px solid ${FOCUS_ACCENT}; box-shadow: 0 0 0 3px rgba(13, 77, 45, 0.25);`
            : `border: 2px solid ${dimmed ? DIM.border : '#ccc'};`;

        let html = `
            <div xmlns="http://www.w3.org/1999/xhtml" style="
                width: ${NODE_WIDTH}px;
                height: ${height}px;
                background: ${bg};
                ${border}
                border-radius: 8px;
                font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
                font-size: 13px;
                overflow: hidden;
            ">
                <a href="/p/${projectID}/hops/${hop.ID}" style="
                    display: block;
                    padding: 10px;
                    font-weight: bold;
                    color: ${dimmed ? DIM.ink : '#333'};
                    text-decoration: none;
                    border-bottom: 1px solid rgba(0,0,0,0.1);
                    white-space: nowrap;
                    overflow: hidden;
                    text-overflow: ellipsis;
                " title="${name}">
                    ${name}
                    ${hop.Status !== 'pending' ? `<span style="
                        font-size: 10px;
                        font-weight: normal;
                        color: ${dimmed ? DIM.ink : (INK[hop.Status] || '#666')};
                        margin-left: 5px;
                    ">${escapeHTML(hop.Status)}</span>` : ''}
                </a>
        `;

        if (hop.Status === 'pending') {
            html += `<div style="padding: 12px 10px; font-style: italic; color: #666; font-size: 11px;">Hop pending</div>`;
        } else if (hop.Variations && hop.Variations.length > 0) {
            html += `<div style="padding: 5px 10px;">`;
            hop.Variations.forEach(v => {
                const isProposed = !v.ID;
                const isMerged = v.Status === 'merged';
                const isRejected = v.Status === 'rejected';
                const tag = isProposed ? 'span' : 'a';
                const href = isProposed ? '' : `href="/p/${projectID}/variations/${v.ID}"`;

                const isFocus = view.focusVariationID && v.ID === view.focusVariationID;

                // Merged and rejected share the green fill; rejected fades its
                // text, because it lost to a sibling rather than failing.
                let fill, ink;
                if (isFocus) {
                    // The Variation being viewed is filled with the accent
                    // rather than tinted with it: on a panel of greys it must
                    // be unmistakable, not merely different.
                    fill = FOCUS_ACCENT;
                    ink = '#fff';
                } else if (view.dim) {
                    fill = DIM.variationFill;
                    ink = DIM.variationInk;
                } else if (isMerged || isRejected) {
                    fill = FILL.merged;
                    ink = isRejected ? 'rgba(21, 87, 36, 0.5)' : INK.merged;
                } else {
                    fill = FILL[v.Status] || '#e9ecef';
                    ink = INK[v.Status] || '#666';
                }

                html += `
                    <${tag} ${href} style="
                        display: block;
                        padding: 3px 6px;
                        margin: 2px 0;
                        background: ${fill};
                        border-radius: 4px;
                        color: ${ink};
                        text-decoration: none;
                        font-size: 11px;
                        font-style: ${isProposed ? 'italic' : 'normal'};
                        font-weight: ${isFocus || (isMerged && !view.dim) ? 'bold' : 'normal'};
                        white-space: nowrap;
                        overflow: hidden;
                        text-overflow: ellipsis;
                    " title="${escapeHTML(v.Name)} (${escapeHTML(v.Status)})">
                        ${escapeHTML(v.Name)}${isProposed ? ' (proposed)' : ''}
                    </${tag}>
                `;
            });
            html += `</div>`;
        }

        return html + `</div>`;
    }

    /**
     * Render the roadmap into a container.
     *
     * @param {HTMLElement} container
     * @param {Object} opts
     *   hops         {Array}   hop views, each {ID, Name, Status, Variations}
     *   edges        {Array}   {from, to} hop-ID pairs
     *   projectID    {string}
     *   focusHopID   {string=} hop to ring and center on
     *   focusVariationID {string=} variation to fill with the accent and, when
     *                          the hop is taller than the container, scroll to
     *   dimOthers    {bool=}   grey everything that is not focused, so the one
     *                          thing being viewed carries the only colour
     *   scale        {number=} initial zoom (default 1)
     *   fitFocus     {bool=}   shrink to fit the focused hop in the container
     *                          before centering (default false)
     *   wheelPan     {bool=}   swallow plain wheel events to pan (default true).
     *                          Must be false when embedded in a scrolling page,
     *                          or the panel traps the page's scroll.
     * @returns {Object|null} controller with center() and fit(), or null if
     *   there was nothing to draw.
     */
    function render(container, opts) {
        opts = opts || {};
        const hops = opts.hops || [];
        const edges = opts.edges || [];
        const projectID = opts.projectID;
        const focusHopID = opts.focusHopID || null;
        const focusVariationID = opts.focusVariationID || null;

        container.innerHTML = '';
        if (hops.length === 0) {
            container.innerHTML = '<p class="empty" style="text-align: center; padding: 50px;">No hops in roadmap yet.</p>';
            return null;
        }

        const g = new dagre.graphlib.Graph();
        g.setGraph({ rankdir: 'LR', nodesep: 50, ranksep: 80, marginx: 20, marginy: 20 });
        g.setDefaultEdgeLabel(() => ({}));

        hops.forEach(hop => {
            g.setNode(hop.ID, { width: NODE_WIDTH, height: nodeHeight(hop), hop: hop });
        });
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
        svg.setAttribute('height', graphHeight);
        svg.style.display = 'block';

        const defs = document.createElementNS('http://www.w3.org/2000/svg', 'defs');
        defs.innerHTML = '<marker id="roadmap-arrowhead" markerWidth="10" markerHeight="7" refX="9" refY="3.5" orient="auto"><polygon points="0 0, 10 3.5, 0 7" fill="#999"/></marker>';
        svg.appendChild(defs);

        // Edges first, so nodes paint over them.
        g.edges().forEach(e => {
            const edge = g.edge(e);
            const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
            let d = `M ${edge.points[0].x} ${edge.points[0].y}`;
            for (let i = 1; i < edge.points.length; i++) {
                d += ` L ${edge.points[i].x} ${edge.points[i].y}`;
            }
            path.setAttribute('d', d);
            path.setAttribute('fill', 'none');
            path.setAttribute('stroke', '#999');
            path.setAttribute('stroke-width', '2');
            path.setAttribute('marker-end', 'url(#roadmap-arrowhead)');
            svg.appendChild(path);
        });

        let focusNode = null;
        g.nodes().forEach(nodeId => {
            const node = g.node(nodeId);
            const hop = node.hop;
            const focused = focusHopID && hop.ID === focusHopID;
            if (focused) focusNode = node;

            const fo = document.createElementNS('http://www.w3.org/2000/svg', 'foreignObject');
            fo.setAttribute('x', node.x - node.width / 2);
            fo.setAttribute('y', node.y - node.height / 2);
            fo.setAttribute('width', node.width);
            // The ring is drawn outside the node box, so the foreignObject needs
            // room for it or it gets clipped.
            fo.setAttribute('height', node.height + 8);
            fo.innerHTML = hopNodeHTML(hop, node.height, {
                projectID: projectID,
                focused: focused,
                dim: !!opts.dimOthers,
                focusVariationID: focusVariationID,
            });
            svg.appendChild(fo);
        });

        container.appendChild(svg);

        const view = panZoom(container, svg, {
            scale: opts.scale || 1,
            wheelPan: opts.wheelPan !== false,
            graphWidth: Math.max(graphWidth, container.clientWidth),
            graphHeight: graphHeight,
        });

        const FIT_PADDING = 16;

        // Where the focused Variation sits inside its Hop node.
        function variationY() {
            if (!focusVariationID || !focusNode.hop.Variations) return null;
            const i = focusNode.hop.Variations.findIndex(v => v.ID === focusVariationID);
            if (i < 0) return null;
            return focusNode.y - focusNode.height / 2
                + VARIATION_TOP + i * VARIATION_HEIGHT + VARIATION_HEIGHT / 2;
        }

        // Shrink until the focused hop fits, but never enlarge and never past
        // the point where the labels stop being readable.
        function fitScale() {
            const s = Math.min(
                1,
                (container.clientHeight - FIT_PADDING) / focusNode.height,
                (container.clientWidth - FIT_PADDING) / focusNode.width
            );
            return Math.max(s, MIN_FIT_SCALE);
        }

        const controller = {
            /**
             * Put the focused hop in view: shrink until it fits, then centre
             * it. A hop with more variations than the panel can show even at
             * the minimum readable scale is centred on the variation being
             * viewed instead, so the row the reader came for is never the part
             * that falls off the edge.
             *
             * A reader who has zoomed by hand has said what they want to see,
             * so expanding the panel afterwards re-centres without overriding
             * their zoom.
             */
            center: function() {
                if (!focusNode) { view.reset(); return; }

                let scale = null;
                if (opts.fitFocus && !view.userZoomed()) {
                    scale = fitScale();
                    view.setScale(scale);
                }

                let y = focusNode.y;
                if (scale !== null && focusNode.height * scale > container.clientHeight) {
                    y = variationY() || y;
                }
                view.centerOn(focusNode.x, y);
            },
            hasFocus: function() { return focusNode !== null; },
        };
        controller.center();
        return controller;
    }

    function panZoom(container, svg, cfg) {
        const wheelPan = cfg.wheelPan;
        let scale = cfg.scale;
        let panX = 0;
        let panY = 0;
        let isPanning = false;
        let startX, startY;
        // Set once the reader zooms by hand, so automatic fitting stops
        // overriding a zoom they chose deliberately.
        let zoomedByHand = false;

        function apply() {
            svg.style.transform = `translate(${panX}px, ${panY}px) scale(${scale})`;
            svg.style.transformOrigin = '0 0';
        }
        apply();

        container.addEventListener('wheel', (e) => {
            if (e.ctrlKey || e.shiftKey) {
                e.preventDefault();
                const delta = e.deltaY > 0 ? 0.95 : 1.05;
                const newScale = Math.min(Math.max(scale * delta, 0.25), 3);

                // Zoom toward the cursor.
                const rect = container.getBoundingClientRect();
                const x = e.clientX - rect.left;
                const y = e.clientY - rect.top;
                panX = x - (x - panX) * (newScale / scale);
                panY = y - (y - panY) * (newScale / scale);
                scale = newScale;
                zoomedByHand = true;
                apply();
            } else if (wheelPan) {
                e.preventDefault();
                panX -= e.deltaX;
                panY -= e.deltaY;
                apply();
            }
            // Otherwise let the event through: an embedded panel must not
            // capture the page's scroll.
        }, { passive: false });

        container.addEventListener('mousedown', (e) => {
            if (e.target.tagName === 'A') return; // Let links be clicked.
            isPanning = true;
            startX = e.clientX - panX;
            startY = e.clientY - panY;
            container.style.cursor = 'grabbing';
        });
        document.addEventListener('mousemove', (e) => {
            if (!isPanning) return;
            panX = e.clientX - startX;
            panY = e.clientY - startY;
            apply();
        });
        document.addEventListener('mouseup', () => {
            if (!isPanning) return;
            isPanning = false;
            container.style.cursor = 'grab';
        });

        // Place a graph point in the middle of the container, along one axis.
        // An axis where the whole graph already fits is centered outright —
        // otherwise expanding the panel just adds blank space beneath the
        // graph. On an axis that overflows, the offset is clamped so the pan
        // never runs off either end into emptiness.
        function offsetFor(point, containerSize, graphSize) {
            const drawn = graphSize * scale;
            if (drawn <= containerSize) return (containerSize - drawn) / 2;
            const wanted = containerSize / 2 - point * scale;
            return Math.max(Math.min(wanted, 0), containerSize - drawn);
        }

        return {
            centerOn: function(x, y) {
                panX = offsetFor(x, container.clientWidth, cfg.graphWidth);
                panY = offsetFor(y, container.clientHeight, cfg.graphHeight);
                apply();
            },
            reset: function() { panX = 0; panY = 0; apply(); },
            setScale: function(s) { scale = s; apply(); },
            userZoomed: function() { return zoomedByHand; },
        };
    }

    return { render: render };
})();
