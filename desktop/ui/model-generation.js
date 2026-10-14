/* Show persisted generation provenance, never infer the deployed model policy. */
(function () {
  "use strict";
  window.ArkismModelGeneration = ({ el }) => record => {
    const generator=record?.generator;
    if (!generator || typeof generator.model !== "string" || !generator.model) return null;
    const panel=el("section","memory-context-panel model-generation");panel.setAttribute("aria-label","생성 모델 기록");
    panel.append(el("p","","생성 모델 기록"),el("p","",generator.model));
    if(generator.prompt_version)panel.append(el("p","meta",`프롬프트 버전   ${generator.prompt_version}`));
    if(generator.pipeline_version)panel.append(el("p","meta",`처리 버전   ${generator.pipeline_version}`));
    panel.append(el("p","meta","이 기록을 생성한 모델이에요. 모델 이름만으로 내용의 정확성이나 현재 기본 모델을 판단하지 않아요."));
    return panel;
  };
})();
