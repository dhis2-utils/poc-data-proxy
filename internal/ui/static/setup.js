// Scans the installation card's QR code into the central address field, where the
// browser can (the BarcodeDetector API and a camera on a secure page); elsewhere the
// address is typed.
(function () {
  if (!('BarcodeDetector' in window) || !navigator.mediaDevices || !window.isSecureContext) return;
  var row = document.getElementById('scan-row'), btn = document.getElementById('scan');
  var video = document.getElementById('scan-video'), field = document.getElementById('c');
  if (!row || !btn || !video || !field) return;
  row.hidden = false;
  btn.addEventListener('click', function () {
    var detector = new BarcodeDetector({ formats: ['qr_code'] });
    navigator.mediaDevices.getUserMedia({ video: { facingMode: 'environment' } }).then(function (stream) {
      video.srcObject = stream;
      video.hidden = false;
      video.play();
      var stopped = false;
      var stop = function () { stopped = true; stream.getTracks().forEach(function (t) { t.stop(); }); video.hidden = true; };
      setTimeout(stop, 120000); // the camera does not stay on
      var look = function () {
        if (stopped) return;
        detector.detect(video).then(function (codes) {
          var hit = codes.map(function (c) { return c.rawValue.trim(); }).filter(function (v) { return v.indexOf('https://') === 0; })[0];
          if (hit) { field.value = hit; stop(); return; }
          setTimeout(look, 300);
        }).catch(function () { setTimeout(look, 300); });
      };
      look();
    }).catch(function () { row.hidden = true; });
  });
})();
