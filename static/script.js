
(function () {
    const userID = crypto.randomUUID().replace(/-/g, "").slice(0, 12);
    document.getElementById("userBadge").textContent = "user: " + userID;

    let movies = [];
    let selectedMovie = null;
    let activeSession = null; // { sessionID, movieID, seatID, expiresAt }
    let pollInterval = null;
    let timerInterval = null;

    // --- API helpers ---

    function api(method, path, body) {
        const opts = {
            method,
            headers: { "Content-Type": "application/json" },
        };
        if (body) opts.body = JSON.stringify(body);
        return fetch(path, opts).then(function (r) {
            if (r.status === 204) return null;
            return r.json().then(function (data) {
                if (!r.ok) throw new Error(data.error || "request failed");
                return data;
            });
        });
    }

    // --- Movie list ---

    function loadMovies() {
        api("GET", "/movies").then(function (data) {
            movies = data;
            renderMovies();
        });
    }

    function renderMovies() {
        var container = document.getElementById("movieList");
        container.innerHTML = "";
        movies.forEach(function (m) {
            var card = document.createElement("div");
            card.className =
                "movie-card" +
                (selectedMovie && selectedMovie.id === m.id ? " selected" : "");
            card.innerHTML =
                "<h3>" +
                escapeHtml(m.title) +
                "</h3><p>" +
                m.rows +
                " rows &times; " +
                m.seats_per_row +
                " seats</p>";
            card.addEventListener("click", function () {
                selectMovie(m);
            });
            container.appendChild(card);
        });
    }

    function releaseActiveSession() {
        if (!activeSession) return Promise.resolve();
        var sid = activeSession.sessionID;
        clearTimerInterval();
        activeSession = null;
        document.getElementById("checkoutArea").innerHTML = "";
        return api("DELETE", "/sessions/" + sid, { user_id: userID }).catch(
            function () { },
        );
    }

    function selectMovie(movie) {
        releaseActiveSession();
        selectedMovie = movie;
        renderMovies();
        document.getElementById("mainContent").style.display = "flex";
        document.getElementById("checkoutArea").innerHTML = "";
        fetchSeats();
        startPolling();
    }

    // --- Seat grid ---

    function fetchSeats() {
        if (!selectedMovie) return;
        api("GET", "/movies/" + selectedMovie.id + "/seats").then(
            function (seats) {
                renderGrid(seats);
            },
        );
    }

    function renderGrid(seatStatuses) {
        var grid = document.getElementById("seatGrid");
        grid.innerHTML = "";
        var statusMap = {};
        seatStatuses.forEach(function (s) {
            statusMap[s.seat_id] = s;
        });

        var rowLabels = "ABCDEFGHIJKLMNOPQRSTUVWXYZ";
        for (var r = 0; r < selectedMovie.rows; r++) {
            var rowDiv = document.createElement("div");
            rowDiv.className = "seat-row";
            var label = document.createElement("div");
            label.className = "row-label";
            label.textContent = rowLabels[r];
            rowDiv.appendChild(label);

            for (var s = 1; s <= selectedMovie.seats_per_row; s++) {
                var seatID = rowLabels[r] + s;
                var btn = document.createElement("button");
                btn.className = "seat";
                btn.textContent = s;
                btn.dataset.seatId = seatID;

                var info = statusMap[seatID];
                if (info) {
                    if (info.confirmed) {
                        btn.classList.add("seat--confirmed");
                    } else if (info.booked && info.user_id === userID) {
                        btn.classList.add("seat--held-mine");
                    } else if (info.booked) {
                        btn.classList.add("seat--held-other");
                    }
                }

                (function (id) {
                    btn.addEventListener("click", function () {
                        holdSeat(id);
                    });
                })(seatID);

                rowDiv.appendChild(btn);
            }

            var labelRight = document.createElement("div");
            labelRight.className = "row-label";
            labelRight.textContent = rowLabels[r];
            rowDiv.appendChild(labelRight);
            grid.appendChild(rowDiv);
        }
    }

    // --- Hold / Confirm / Release ---

    function holdSeat(seatID) {
        if (activeSession) return; // already holding a seat
        api(
            "POST",
            "/movies/" + selectedMovie.id + "/seats/" + seatID + "/hold",
            { user_id: userID },
        )
            .then(function (data) {
                activeSession = {
                    sessionID: data.session_id,
                    movieID: data.movie_id,
                    seatID: data.seat_id,
                    expiresAt: new Date(data.expires_at),
                };
                fetchSeats();
                renderCheckout();
                startTimer();
            })
            .catch(function (err) {
                showCheckoutStatus(err.message, "error");
            });
    }

    function confirmSeat() {
        if (!activeSession) return;
        api("PUT", "/sessions/" + activeSession.sessionID + "/confirm", {
            user_id: userID,
        })
            .then(function () {
                clearTimerInterval();
                activeSession = null;
                fetchSeats();
                showCheckoutStatus("Confirmed!", "success");
            })
            .catch(function (err) {
                showCheckoutStatus(err.message, "error");
            });
    }

    function releaseSeat() {
        if (!activeSession) return;
        api("DELETE", "/sessions/" + activeSession.sessionID, {
            user_id: userID,
        })
            .then(function () {
                clearTimerInterval();
                activeSession = null;
                fetchSeats();
                document.getElementById("checkoutArea").innerHTML = "";
            })
            .catch(function (err) {
                showCheckoutStatus(err.message, "error");
            });
    }

    // --- Checkout panel ---

    function renderCheckout() {
        if (!activeSession) return;
        var area = document.getElementById("checkoutArea");
        area.innerHTML =
            '<div class="checkout">' +
            "<h3>Checkout</h3>" +
            '<div class="checkout-info"><span>Seat:</span> ' +
            escapeHtml(activeSession.seatID) +
            "</div>" +
            '<div class="checkout-info"><span>Movie:</span> ' +
            escapeHtml(activeSession.movieID) +
            "</div>" +
            '<div class="checkout-info"><span>Session:</span> ' +
            activeSession.sessionID.slice(0, 8) +
            "&hellip;</div>" +
            '<div class="timer" id="timer">--:--</div>' +
            '<div class="checkout-buttons">' +
            '<button class="btn btn--confirm" id="btnConfirm">Confirm</button>' +
            '<button class="btn btn--release" id="btnRelease">Release</button>' +
            "</div>" +
            '<div id="checkoutStatus"></div>' +
            "</div>";
        document
            .getElementById("btnConfirm")
            .addEventListener("click", confirmSeat);
        document
            .getElementById("btnRelease")
            .addEventListener("click", releaseSeat);
    }

    function showCheckoutStatus(msg, type) {
        var area = document.getElementById("checkoutArea");
        area.innerHTML =
            '<div class="checkout">' +
            '<div class="status-msg ' +
            type +
            '">' +
            escapeHtml(msg) +
            "</div>" +
            "</div>";
        setTimeout(function () {
            if (!activeSession) area.innerHTML = "";
        }, 3000);
    }

    // --- Timer ---

    function startTimer() {
        clearTimerInterval();
        updateTimer();
        timerInterval = setInterval(updateTimer, 1000);
    }

    function updateTimer() {
        if (!activeSession) {
            clearTimerInterval();
            return;
        }
        var el = document.getElementById("timer");
        if (!el) return;
        var remaining = Math.max(
            0,
            Math.floor((activeSession.expiresAt - Date.now()) / 1000),
        );
        var mins = Math.floor(remaining / 60);
        var secs = remaining % 60;
        el.textContent =
            String(mins).padStart(2, "0") + ":" + String(secs).padStart(2, "0");

        if (remaining < 60) {
            el.classList.add("urgent");
        } else {
            el.classList.remove("urgent");
        }

        if (remaining <= 0) {
            clearTimerInterval();
            activeSession = null;
            fetchSeats();
            showCheckoutStatus("Hold expired", "error");
        }
    }

    function clearTimerInterval() {
        if (timerInterval) {
            clearInterval(timerInterval);
            timerInterval = null;
        }
    }

    // --- Polling ---

    function startPolling() {
        if (pollInterval) clearInterval(pollInterval);
        pollInterval = setInterval(fetchSeats, 2000);
    }

    // --- Util ---

    function escapeHtml(str) {
        var div = document.createElement("div");
        div.textContent = str;
        return div.innerHTML;
    }

    // --- Init ---

    loadMovies();
})();
