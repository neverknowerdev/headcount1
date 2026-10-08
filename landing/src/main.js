(() => {
  const $ = (s, r = document) => r.querySelector(s);
  const $$ = (s, r = document) => [...r.querySelectorAll(s)];
  const reduce = matchMedia('(prefers-reduced-motion: reduce)').matches;
  const TICK = 1100;

  // Runs fn(k) every TICK, but only while el is on screen and the tab is visible.
  function ticker(el, fn) {
    let k = 0, id = 0, seen = false;
    const sync = () => {
      const run = seen && !document.hidden;
      if (run && !id) id = setInterval(() => fn(++k), TICK);
      else if (!run && id) { clearInterval(id); id = 0; }
    };
    new IntersectionObserver(es => { seen = es[es.length - 1].isIntersecting; sync(); }).observe(el);
    document.addEventListener('visibilitychange', sync);
    fn(0);
  }

  // Savings: bars grow once when the section scrolls in; Replay restarts them.
  const cost = $('#cost');
  if (cost && !reduce) {
    cost.classList.add('anim');
    const io = new IntersectionObserver(es => {
      if (es.some(e => e.isIntersecting)) { cost.classList.add('go'); io.disconnect(); }
    }, { threshold: 0.25 });
    io.observe(cost);
    $('.replay', cost).addEventListener('click', () => {
      cost.classList.remove('go');
      void cost.offsetWidth;
      cost.classList.add('go');
    });
  }

  // Router: cycles through work types and shows the model picked for each.
  const router = $('.router-body');
  if (router) {
    const work = $$('.r-item.w', router), models = $$('.r-item.m', router);
    const out = k => $(`[data-r="${k}"]`, router);
    const show = k => {
      const i = k % work.length, d = work[i].dataset;
      work.forEach((el, j) => el.classList.toggle('on', j === i));
      models.forEach((el, j) => el.classList.toggle('on', j === i));
      out('work').textContent = work[i].firstElementChild.textContent.toLowerCase();
      out('model').textContent = models[i].firstElementChild.textContent;
      out('fit').textContent = d.fit;
      out('fitbar').style.transform = `scaleX(${d.fit})`;
      out('price').textContent = d.price;
      out('list').textContent = d.list;
    };
    reduce ? show(0) : ticker(router, show);
  }

  // Council: seats weigh in one by one, then the decision lands.
  const council = $('.council');
  if (council) {
    const seats = $$('.seat', council), decision = $('.decision', council);
    const show = k => {
      const c = k % 8;
      seats.forEach((el, i) => el.classList.toggle('on', c > i));
      decision.classList.toggle('on', c >= 4);
    };
    reduce ? show(7) : ticker(council, show);
  }

  // A/B: three runs race, then the winner is marked.
  const ab = $('.ab');
  if (ab) {
    const fills = $$('.run-fill', ab), cap = $('.cap', ab);
    const show = k => {
      const a = k % 9;
      fills.forEach(el => { el.style.transform = `scaleX(${Math.min(1, a * el.dataset.rate / 100)})`; });
      ab.classList.toggle('done', a >= 4);
      cap.classList.toggle('on', a >= 5);
    };
    reduce ? show(8) : ticker(ab, show);
  }
})();
