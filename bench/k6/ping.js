// k6 counterpart of the Tidegate /ping benchmark: one GET per iteration,
// keep-alive, no checks, no sleep, response bodies discarded.
// VUs and duration are passed on the command line (--vus, --duration).
import http from 'k6/http';

export const options = {
  discardResponseBodies: true,
};

const url = __ENV.URL || 'http://127.0.0.1:18080/ping';

export default function () {
  http.get(url);
}
